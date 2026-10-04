import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import image_publication_policy as policy

SHA = "a" * 40
BASE = "b" * 40
REPO = "rzp-labs/vexxx-stash"


class ImagePublicationPolicyTests(unittest.TestCase):
    def plan(self, name="push", kind="branch", ref="master", paths=(), event=None, **kwargs):
        return policy.plan(name, kind, ref, SHA, REPO, event or {}, paths, **kwargs)

    def test_master_and_pull_requests_never_publish(self):
        for name in ("push", "pull_request"):
            for paths in (["pkg/ffmpeg/options.go"], [".github/workflows/docker-publish.yml"], ["README.md"]):
                with self.subTest(name=name, paths=paths):
                    result = self.plan(name=name, paths=paths)
                    self.assertFalse(result["publish"])
                    self.assertEqual(result["image_ref"], "")

    def test_only_exact_normal_release_versions_publish(self):
        for version in ("v0.0.0", "v0.31.0", "v12.345.678"):
            result = self.plan(kind="tag", ref=version)
            self.assertTrue(result["publish"])
            self.assertTrue(result["run_tests"] and result["build_image"])
            self.assertEqual(result["image_ref"], f"ghcr.io/{REPO}:{version}")
            self.assertEqual(result["sha_ref"], f"ghcr.io/{REPO}:sha-{SHA}")
            self.assertEqual(result["version"], version)
        for version in ("v1", "v1.2", "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-rc.1",
                        "v1.2.3+build", "v1.2.3/foo", "v1.2.3\n", "v" + "9" * 129 + ".0.0"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                self.plan(kind="tag", ref=version)

    def test_manual_default_only_validates(self):
        for inputs in ({}, {"publish_test_image": False}, {"publish_test_image": "false"}):
            result = self.plan(name="workflow_dispatch", event={"inputs": inputs})
            self.assertTrue(result["run_tests"] and result["build_image"])
            self.assertFalse(result["publish"])
            self.assertEqual(result["image_ref"], "")

    def test_deliberate_manual_publication_is_isolated_and_unique(self):
        result = self.plan(name="workflow_dispatch", ref="master", event={
            "inputs": {"publish_test_image": "true", "test_label": "intel-candidate"}},
            run_id="12345", attempt="2")
        self.assertEqual(result["image_ref"], f"ghcr.io/{REPO}-test:test-intel-candidate-12345-2")
        self.assertEqual(result["version"], "test-intel-candidate-12345-2")
        self.assertEqual(result["sha_ref"], "")
        for label in ("", "edge", "latest", "../release", "UPPER", "two--hyphens", "a" * 33, "bad\nlabel"):
            # Even reserved-looking labels stay in the test package/tag namespace.
            if label in ("edge", "latest"):
                r = self.plan(name="workflow_dispatch", event={"inputs": {"publish_test_image": True, "test_label": label}})
                self.assertIn("-test:test-", r["image_ref"])
            else:
                with self.subTest(label=label), self.assertRaises(ValueError):
                    self.plan(name="workflow_dispatch", event={"inputs": {"publish_test_image": True, "test_label": label}})
        with self.assertRaises(ValueError):
            self.plan(name="workflow_dispatch", event={"inputs": {"publish_test_image": "yes"}})

    def test_application_changes_do_not_package(self):
        for name in ('push', 'pull_request'):
            for path in ('pkg/ffmpeg/options.go', 'internal/api/server.go', 'go.mod', 'go.sum',
                         'ui/v2.5/src/App.tsx', 'ui/v2.5/pnpm-lock.yaml',
                         'graphql/schema/config.graphql', 'scripts/gpu_generation/benchmark.py'):
                result = self.plan(name=name, paths=[path])
                self.assertTrue(result['run_tests'], path)
                self.assertFalse(result['build_image'], path)

    def test_latest_publication_is_deliberate_master_only_and_uses_application_package(self):
        event = {'inputs': {'publish_latest': 'true', 'test_label': 'runtime19'}}
        result = self.plan(name='workflow_dispatch', event=event, run_id='123', attempt='2')
        self.assertEqual(result['image_ref'], f'ghcr.io/{REPO}:candidate-runtime19-123-2')
        self.assertEqual(result['latest_ref'], f'ghcr.io/{REPO}:latest')
        self.assertEqual(result['version'], 'candidate-runtime19-123-2')
        self.assertTrue(result['publish'] and result['build_image'])
        self.assertEqual(result['sha_ref'], '')
        for kind, ref in (('branch', 'feature'), ('tag', 'v1.2.3')):
            with self.assertRaises(ValueError):
                self.plan(name='workflow_dispatch', kind=kind, ref=ref, event=event)
        for inputs in ({'publish_latest': True, 'publish_test_image': True, 'test_label': 'candidate'},
                       {'publish_latest': True}, {'publish_latest': 'yes'}, {'publish_latest': 1}):
            with self.assertRaises(ValueError):
                self.plan(name='workflow_dispatch', event={'inputs': inputs})
        for name, kind, ref in (('push', 'branch', 'master'), ('pull_request', 'branch', 'feature'),
                                ('push', 'tag', 'v1.2.3')):
            self.assertEqual(self.plan(name=name, kind=kind, ref=ref, paths=['pkg/ffmpeg/source.go'])['latest_ref'], '')
        self.assertEqual(self.plan(name='workflow_dispatch', event={'inputs': {'publish_latest': False}})['latest_ref'], '')

    def test_domains_documentation_packaging_and_unknown_inputs(self):
        for paths in (['docs/DEVELOPMENT.md'], ['AGENTS.md', 'REVIEW.md'],
                      ['ui/v2.5/README.md', 'pkg/sqlite/migrations/README.md'],
                      ['docker/production/README.md', 'docker/build/x86_64/README.md']):
            self.assertEqual(policy.affected(paths), (False, False))
        for path in ('Makefile', '.dockerignore', 'scripts/check_intel_runtime.py',
                     'docker/build/x86_64/Dockerfile', 'docker/production/docker-compose.yml'):
            self.assertEqual(policy.affected([path]), (True, True), path)
        backend = policy.obligations(['pkg/ffmpeg/options.go'])
        self.assertTrue(backend['backend'])
        self.assertFalse(backend['frontend'])
        ui = policy.obligations(['ui/v2.5/src/App.tsx'])
        self.assertTrue(ui['frontend'])
        self.assertFalse(ui['backend'])
        self.assertTrue(policy.obligations(['ui/v2.5/src/locales/en-GB.json'])['backend'])
        self.assertTrue(policy.obligations(['scripts/test_check_intel_runtime.py'])['python'])
        for path in ('new-build.conf', 'graphql/schema/config.graphql',
                     '.github/workflows/docker-publish.yml', 'scripts/generateLoginLocales.go'):
            groups = policy.obligations([path])
            self.assertTrue(groups['backend'] and groups['frontend'] and groups['python'], path)
            self.assertFalse(groups['build_image'], path)

    def test_drafts_defer_and_ready_transition_selects_validation(self):
        for draft in (True, False):
            result = self.plan(name='pull_request', paths=['docker/build/x86_64/Dockerfile'],
                               event={'pull_request': {'draft': draft}})
            self.assertEqual(result['deferred'], draft)
            self.assertEqual(result['run_tests'], not draft)
            self.assertEqual(result['build_image'], not draft)

    def test_manual_publication_requires_master(self):
        for kind, ref in (('branch', 'feature'), ('tag', 'v1.2.3')):
            with self.assertRaises(ValueError):
                self.plan(name='workflow_dispatch', kind=kind, ref=ref,
                          event={'inputs': {'publish_test_image': True, 'test_label': 'candidate'}})

    def test_merge_integration_paths_and_missing_history(self):
        event = {'pull_request': {'base': {'sha': BASE}, 'head': {'sha': SHA}}}
        merge = 'c' * 40
        with patch.object(policy.subprocess, 'check_output', side_effect=[
                (BASE + '\n').encode(), b'docs/new.md\0', b'ui/v2.5/src/App.tsx\0']) as run:
            paths = policy.changed_paths('pull_request', event, Path.cwd(), merge)
        self.assertTrue(policy.obligations(paths)['frontend'])
        self.assertEqual(run.call_args_list[-1].args[0],
                         ['git', 'diff', '--no-renames', '--name-only', '-z', SHA, merge])
        with patch.object(policy.subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, 'git')):
            with self.assertRaises(subprocess.CalledProcessError):
                policy.changed_paths('pull_request', event, Path.cwd(), merge)
        with patch.object(policy.subprocess, 'check_output', side_effect=[(BASE + '\n').encode(),
                                                                                 (merge + '\n').encode()]):
            with self.assertRaises(ValueError):
                policy.checkout_identity(Path.cwd(), SHA)

    def test_deleted_refs_do_not_publish(self):
        self.assertFalse(self.plan(kind="tag", ref="v1.2.3", event={"deleted": True})["publish"])

    def test_diff_uses_full_merge_base_and_both_rename_sides(self):
        event = {"pull_request": {"base": {"sha": BASE}, "head": {"sha": SHA}}}
        with patch.object(policy.subprocess, "check_output", side_effect=[(BASE + "\n").encode(),
                                                                      b"pkg/deleted.go\0docs/new.md\0"]) as run:
            paths = policy.changed_paths("pull_request", event, Path.cwd())
        self.assertEqual(paths, ["pkg/deleted.go", "docs/new.md"])
        self.assertEqual(run.call_args_list[1].args[0],
                         ["git", "diff", "--no-renames", "--name-only", "-z", BASE, SHA])
        self.assertEqual(policy.affected(paths), (True, False))
        with self.assertRaises(ValueError):
            policy.changed_paths("push", {"before": "--option", "after": SHA}, Path.cwd())

    def test_push_range_and_initial_push_keep_all_changed_names(self):
        for base, expected_command in (
                (BASE, ['git', 'diff', '--no-renames', '--name-only', '-z', BASE, SHA]),
                ('0' * 40, ['git', 'ls-tree', '-r', '--name-only', '-z', SHA])):
            with patch.object(policy.subprocess, 'check_output', return_value=b'docs/name\nwith-newline.md\0pkg/change.go\0') as run:
                paths = policy.changed_paths('push', {'before': base, 'after': SHA}, Path.cwd())
            self.assertEqual(run.call_args.args[0], expected_command)
            self.assertEqual(paths, ['docs/name\nwith-newline.md', 'pkg/change.go'])
            self.assertTrue(policy.obligations(paths)['backend'])

    def test_cli_requires_complete_gate_and_outputs_safe_values(self):
        with tempfile.TemporaryDirectory() as directory:
            event, output = Path(directory) / "event.json", Path(directory) / "output"
            event.write_text(json.dumps({"inputs": {"publish_test_image": "true", "test_label": "candidate"}}))
            checkout = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
            env = {**os.environ, "GITHUB_EVENT_PATH": str(event), "GITHUB_OUTPUT": str(output),
                   "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF_TYPE": "branch",
                   "GITHUB_REF_NAME": "master", "GITHUB_SHA": checkout, "GITHUB_REPOSITORY": REPO,
                   "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1",
                   "GITHUB_STEP_SUMMARY": str(Path(directory) / "summary")}
            subprocess.run([sys.executable, str(Path(policy.__file__))], env=env, check=True, capture_output=True)
            self.assertIn("publish=true\n", output.read_text())
            self.assertIn(f"image_ref=ghcr.io/{REPO}-test:test-candidate-123-1\n", output.read_text())
            self.assertNotIn("edge\n", output.read_text())
            event.write_text(json.dumps({"inputs": {"publish_test_image": "true", "test_label": "../unsafe"}}))
            output.unlink()
            result = subprocess.run([sys.executable, str(Path(policy.__file__))], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())

    @unittest.skipUnless(shutil.which("ruby") and shutil.which("bash"), "offline YAML/shell parsers unavailable")
    def test_workflow_publication_boundary_and_shell_syntax(self):
        workflow = Path(__file__).resolve().parent.parent / ".github/workflows/docker-publish.yml"
        parsed = subprocess.check_output([
            "ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(workflow)])
        document = json.loads(parsed)
        self.assertEqual(document["permissions"], {"contents": "read"})
        self.assertFalse(document["on"]["workflow_dispatch"]["inputs"]["publish_test_image"]["default"])
        self.assertFalse(document['on']['workflow_dispatch']['inputs']['publish_latest']['default'])
        jobs = document["jobs"]
        for name in ("plan", "test", "image"):
            self.assertNotIn("permissions", jobs[name])
            self.assertFalse(any("login-action" in step.get("uses", "") for step in jobs[name]["steps"]))
        checks = jobs["plan"]["steps"][1]["run"]
        self.assertIn("test_image_publication_policy.py", checks)
        self.assertIn("test_check_intel_runtime.py", checks)
        build = next(step for step in jobs["image"]["steps"] if "build-push-action" in step.get("uses", ""))
        self.assertFalse(build["with"]["push"])
        self.assertTrue(build["with"]["load"])
        publisher = jobs["publish"]
        self.assertEqual(publisher["permissions"], {"actions": "read", "packages": "write"})
        self.assertEqual(publisher["needs"], ["plan", "image", "ci-required"])
        self.assertIn("github.ref_type == 'tag'", publisher["if"])
        self.assertIn("inputs.publish_test_image", publisher["if"])
        self.assertIn("inputs.publish_latest", publisher["if"])
        self.assertIn("github.ref == 'refs/heads/master'", publisher["if"])
        self.assertIn('docker tag "$LOCAL_IMAGE" "$LATEST_REF"', publisher['steps'][-1]['run'])
        self.assertFalse(any("checkout" in step.get("uses", "") for step in publisher["steps"]))
        self.assertEqual(jobs['ci-required']['if'], 'always()')
        self.assertEqual(jobs['ci-required']['needs'], ['plan', 'test', 'image'])
        self.assertEqual(document['concurrency']['cancel-in-progress'], "${{ github.event_name == 'pull_request' }}")
        self.assertIn('ready_for_review', document['on']['pull_request']['types'])
        self.assertIn('converted_to_draft', document['on']['pull_request']['types'])
        self.assertEqual(document['env']['POSTHOG_UPLOAD_REQUIRED'], 'false')
        aggregate = jobs['ci-required']['steps'][0]['run']
        for selected in ('true', 'false'):
            for test_result in ('success', 'failure', 'cancelled', 'skipped'):
                for image_selected in ('true', 'false'):
                    for image_result in ('success', 'failure', 'cancelled', 'skipped'):
                        with tempfile.TemporaryDirectory() as directory:
                            env = {**os.environ, 'PLAN_RESULT': 'success', 'DEFERRED': 'false',
                                   'RUN_TESTS': selected, 'TEST_RESULT': test_result,
                                   'BUILD_IMAGE': image_selected, 'IMAGE_RESULT': image_result,
                                   'BACKEND': selected, 'FRONTEND': 'false', 'PYTHON': 'false',
                                   'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary')}
                            result = subprocess.run(['bash'], input=aggregate, text=True, env=env, capture_output=True)
                            expected = test_result == ('success' if selected == 'true' else 'skipped') and image_result == (
                                'success' if image_selected == 'true' else 'skipped')
                            self.assertEqual(result.returncode == 0, expected, (selected, test_result, image_selected, image_result))
        for override in ({'DEFERRED': 'true'}, {'PLAN_RESULT': 'failure'}, {'BACKEND': ''},
                         {'RUN_TESTS': ''}, {'BUILD_IMAGE': 'invalid'}, {'BACKEND': 'true'}):
            with tempfile.TemporaryDirectory() as directory:
                env = {**os.environ, 'PLAN_RESULT': 'success', 'DEFERRED': 'false',
                       'RUN_TESTS': 'false', 'TEST_RESULT': 'skipped', 'BUILD_IMAGE': 'false',
                       'IMAGE_RESULT': 'skipped', 'BACKEND': 'false', 'FRONTEND': 'false', 'PYTHON': 'false',
                       'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary'), **override}
                result = subprocess.run(['bash'], input=aggregate, text=True, env=env, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
        for job in jobs.values():
            for step in job["steps"]:
                if "run" in step:
                    subprocess.run(["bash", "-n"], input=step["run"], text=True, check=True, capture_output=True)


if __name__ == "__main__":
    unittest.main()
