import json
import io
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import image_publication_policy as policy

SHA = "a" * 40
BASE = "b" * 40
REPO = "rzp-labs/vexxx-stash"


class ImagePublicationPolicyTests(unittest.TestCase):
    def plan(self, name="push", kind="branch", ref="master", paths=(), event=None, **kwargs):
        kwargs.setdefault('declared_version', '0.1.0')
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
            result = self.plan(kind="tag", ref=version, declared_version=version[1:])
            self.assertTrue(result["publish"])
            self.assertTrue(result["run_tests"] and result["build_image"])
            self.assertEqual(result["image_ref"], f"ghcr.io/{REPO}:{version}")
            self.assertEqual(result["sha_ref"], f"ghcr.io/{REPO}:sha-{SHA}")
            self.assertEqual(result["version"], version[1:])
            self.assertEqual(result["latest_ref"], f"ghcr.io/{REPO}:latest")
        for version in ("v1", "v1.2", "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-rc.1",
                        "v1.2.3+build", "v1.2.3/foo", "v1.2.3\n", "v" + "9" * 129 + ".0.0"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                self.plan(kind="tag", ref=version)

    def test_release_tag_must_match_its_own_version_file(self):
        with self.assertRaisesRegex(ValueError, 'does not match VERSION'):
            self.plan(kind='tag', ref='v1.2.3', declared_version='0.1.0')
        for version in ('feature-name', 'v0.1.0', '01.2.3', ''):
            with self.assertRaises(ValueError):
                self.plan(declared_version=version)
        dev = self.plan(paths=['VERSION'])
        self.assertEqual(dev['version'], '0.1.0-dev+sha.' + SHA[:12])
        self.assertFalse(dev['publish'])

    def test_diagnostic_label_does_not_change_version_or_image_identity(self):
        for publication in ('publish_latest', 'publish_test_image'):
            plans = [self.plan(name='workflow_dispatch', event={'inputs': {
                publication: True, 'test_label': label}}, run_id='123', attempt='2')
                for label in ('', 'posthog', 'feature-name')]
            self.assertEqual(plans[0], plans[1])
            self.assertEqual(plans[1], plans[2])

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
        self.assertEqual(result["image_ref"], f"ghcr.io/{REPO}-test:test-{SHA[:12]}-12345-2")
        self.assertEqual(result["version"], "0.1.0-dev+sha." + SHA[:12])
        self.assertEqual(result["sha_ref"], "")
        for label in ("edge", "latest", "../release", "UPPER", "two--hyphens", "a" * 33, "bad\nlabel"):
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
        self.assertEqual(result['image_ref'], f'ghcr.io/{REPO}:v0.1.0')
        self.assertEqual(result['latest_ref'], f'ghcr.io/{REPO}:latest')
        self.assertEqual(result['version'], '0.1.0')
        self.assertTrue(result['publish'] and result['build_image'])
        self.assertEqual(result['sha_ref'], f'ghcr.io/{REPO}:sha-{SHA}')
        for kind, ref in (('branch', 'feature'), ('tag', 'v1.2.3')):
            with self.assertRaises(ValueError):
                self.plan(name='workflow_dispatch', kind=kind, ref=ref, event=event)
        for inputs in ({'publish_latest': True, 'publish_test_image': True, 'test_label': 'candidate'},
                       {'publish_latest': 'yes'}, {'publish_latest': 1}):
            with self.assertRaises(ValueError):
                self.plan(name='workflow_dispatch', event={'inputs': inputs})
        for name, kind, ref in (('push', 'branch', 'master'), ('pull_request', 'branch', 'feature'),
                                ('push', 'branch', 'master')):
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

    def test_pr_merge_accepts_pending_or_stale_webhook_metadata(self):
        merge = 'c' * 40
        for metadata in (None, 'd' * 40, merge):
            pr = {'base': {'sha': BASE}, 'head': {'sha': SHA}, 'merge_commit_sha': metadata}
            with patch.object(policy.subprocess, 'check_output',
                              return_value=f'{merge} {BASE} {SHA}\n'.encode()) as run:
                policy.validate_pr_merge(Path.cwd(), pr, merge)
            self.assertEqual(run.call_args.args[0], ['git', 'rev-list', '--parents', '-n', '1', merge])

    def test_pr_merge_rejects_wrong_head_base_or_nonmerge_checkout(self):
        merge = 'c' * 40
        pr = {'base': {'sha': BASE}, 'head': {'sha': SHA}, 'merge_commit_sha': merge}
        for parents in (f'{merge} {BASE}', f'{merge} {BASE} ' + 'd' * 40,
                        f'{merge} ' + 'd' * 40 + f' {SHA}', f'{SHA} {BASE} {SHA}'):
            with patch.object(policy.subprocess, 'check_output', return_value=(parents + '\n').encode()):
                with self.assertRaises(ValueError):
                    policy.validate_pr_merge(Path.cwd(), pr, merge)
        with self.assertRaises(ValueError):
            policy.validate_pr_merge(Path.cwd(), pr, '--option')

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
            self.assertIn(f"image_ref=ghcr.io/{REPO}-test:test-{checkout[:12]}-123-1\n", output.read_text())
            self.assertNotIn("edge\n", output.read_text())
            event.write_text(json.dumps({"inputs": {"publish_test_image": "true", "test_label": "../unsafe"}}))
            output.unlink()
            result = subprocess.run([sys.executable, str(Path(policy.__file__))], env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())

    @unittest.skipUnless(shutil.which("ruby") and shutil.which("bash"), "offline YAML/shell parsers unavailable")
    def test_immutable_publication_registry_guard_fails_closed(self):
        workflow = Path(__file__).resolve().parent.parent / '.github/workflows/docker-publish.yml'
        parsed = subprocess.check_output([
            'ruby', '-ryaml', '-rjson', '-e', 'puts JSON.generate(YAML.load_file(ARGV[0]))', str(workflow)])
        publisher = json.loads(parsed)['jobs']['publish']
        guard = next(step for step in publisher['steps']
                     if step.get('name') == 'Reject overwriting a published release version')
        self.assertEqual(publisher['concurrency']['cancel-in-progress'], False)
        self.assertEqual(guard['if'], "needs.ci-required.outputs.latest_ref != ''")
        self.assertLess(publisher['steps'].index(guard), next(i for i, step in enumerate(publisher['steps'])
                                                            if 'login-action' in step.get('uses', '')))
        code = guard['run'].split("python3 - <<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
        env = {'IMAGE_REF': f'ghcr.io/{REPO}:v0.1.0', 'GHCR_USER': 'dummy', 'GHCR_PASSWORD': 'dummy'}
        def missing(code):
            return urllib.error.HTTPError('https://ghcr.io/manifest', code, 'test', {}, None)
        for status in (404, 200, 302, 401, 403, 429, 500):
            response = io.BytesIO(b'{"token":"dummy"}')
            manifest = io.BytesIO() if status == 200 else missing(status)
            with patch.dict(os.environ, env), patch('urllib.request.OpenerDirector.open', side_effect=[response, manifest]) as request:
                if status == 404:
                    exec(code, {})
                else:
                    with self.assertRaises(SystemExit):
                        exec(code, {})
            self.assertEqual(request.call_args_list[1].args[0].method, 'HEAD')
            self.assertEqual(request.call_args_list[1].args[0].full_url,
                             f'https://ghcr.io/v2/{REPO}/manifests/v0.1.0')
        for token in (b'{}', b'not-json'):
            with patch.dict(os.environ, env), patch('urllib.request.OpenerDirector.open', return_value=io.BytesIO(token)):
                with self.assertRaises(SystemExit):
                    exec(code, {})
        with patch.dict(os.environ, env), patch('urllib.request.OpenerDirector.open', side_effect=missing(401)):
            with self.assertRaises(SystemExit):
                exec(code, {})

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
        validator = jobs['ci-required']
        # runner is available in step contexts, not jobs.<job_id>.env. GitHub
        # rejects the entire workflow before jobs if these paths use it there.
        self.assertNotIn('runner.', json.dumps(validator.get('env', {})))
        self.assertEqual(validator['env']['PACKAGING_EVIDENCE_PATH'], '/tmp/pr-packaging.json')
        evidence_save = next(step for step in validator['steps'] if step.get('name') == 'Save PR packaging evidence')
        self.assertEqual(evidence_save['with']['path'], '${{ env.PACKAGING_EVIDENCE_PATH }}')
        self.assertEqual(validator['permissions'], {'contents': 'read', 'actions': 'read', 'id-token': 'write'})
        self.assertNotIn('needs', validator)  # Required check starts immediately.
        self.assertNotIn('if', validator)  # It must never be skipped for a draft.
        self.assertFalse(any('login-action' in step.get('uses', '') for step in validator['steps']))
        self.assertEqual(sum('checkout' in step.get('uses', '') for step in validator['steps']), 1)
        checks = next(step['run'] for step in validator['steps'] if step.get('id') == 'policy_tests')
        self.assertIn('test_image_publication_policy.py', checks)
        self.assertIn('test_check_intel_runtime.py', checks)
        build = next(step for step in validator['steps'] if 'build-push-action' in step.get('uses', ''))
        self.assertFalse(build["with"]["push"])
        self.assertTrue(build["with"]["load"])
        self.assertEqual(build["with"]["secrets"].strip(),
                         "POSTHOG_CLI_API_KEY=${{ steps.plan.outputs.publish == 'true' && ((github.event_name == 'push' && github.ref_type == 'tag') || (github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/master' && (inputs.publish_test_image || inputs.publish_latest))) && secrets.POSTHOG_CLI_API_KEY || '' }}")
        self.assertIn("POSTHOG_UPLOAD_REQUIRED=${{ steps.plan.outputs.publish }}", build["with"]["build-args"])

        publisher = jobs["publish"]
        self.assertEqual(publisher["permissions"], {"actions": "read", "packages": "write"})
        self.assertEqual(publisher["needs"], 'ci-required')
        self.assertIn("github.ref_type == 'tag'", publisher["if"])
        self.assertIn("inputs.publish_test_image", publisher["if"])
        self.assertIn("inputs.publish_latest", publisher["if"])
        self.assertIn("github.ref == 'refs/heads/master'", publisher["if"])
        self.assertIn('docker tag "$LOCAL_IMAGE" "$LATEST_REF"', publisher['steps'][-1]['run'])
        self.assertFalse(any("checkout" in step.get("uses", "") for step in publisher["steps"]))
        self.assertEqual(document['concurrency']['cancel-in-progress'], "${{ github.event_name == 'pull_request' }}")
        self.assertIn('ready_for_review', document['on']['pull_request']['types'])
        self.assertIn('converted_to_draft', document['on']['pull_request']['types'])
        self.assertEqual(document['env']['POSTHOG_UPLOAD_REQUIRED'], 'false')
        self.assertIn("'validation-deferred' || 'ci-required'", jobs['ci-required']['name'])
        self.assertIn('github.event.pull_request.draft', jobs['ci-required']['name'])
        compiles = [step for step in validator['steps'] if step.get('name', '').startswith('Production ')]
        self.assertEqual(len(compiles), 2)
        for step in compiles:
            self.assertIn("steps.plan.outputs.build_image != 'true'", step['if'])
        aggregate_step = next(step for step in validator['steps'] if step.get('name') == 'Verify every selected obligation')
        self.assertEqual(aggregate_step['if'], 'always()')
        aggregate = aggregate_step['run']
        for selected in ('true', 'false'):
            for test_result in ('success', 'failure', 'cancelled', 'skipped'):
                for image_selected in ('true', 'false'):
                    for image_result in ('success', 'failure', 'cancelled', 'skipped'):
                        with tempfile.TemporaryDirectory() as directory:
                            env = {**os.environ, 'PLAN_RESULT': 'success', 'DEFERRED': 'false',
                                   'RUN_TESTS': selected, 'REQUIRED_TESTS': selected, 'TEST_RESULT': test_result,
                                   'DRAFT_EVENT': 'false', 'REQUIRED_BACKEND': selected,
                                   'REQUIRED_FRONTEND': 'false', 'REQUIRED_PYTHON': 'false',
                                   'REUSED_BACKEND': 'false', 'REUSED_FRONTEND': 'false', 'REUSED_PYTHON': 'false',
                                   'BUILD_IMAGE': image_selected, 'IMAGE_RESULT': image_result,
                                   'PACKAGING_REUSED': 'false', 'EXECUTE_IMAGE': image_selected,
                                   'BACKEND': selected, 'FRONTEND': 'false', 'PYTHON': 'false',
                                   'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary')}
                            env.update(BACKEND_RESULT=test_result,
                                       BACKEND_GENERATE_RESULT='success' if selected == 'true' else 'skipped',
                                       BACKEND_COMPILE_RESULT='success' if selected == 'true' and image_selected == 'false' and test_result == 'success' else 'skipped',
                                       FRONTEND_RESULT='skipped', FRONTEND_GENERATE_RESULT='skipped',
                                       FRONTEND_COMPILE_RESULT='skipped', PYTHON_RESULT='skipped')
                            result = subprocess.run(['bash'], input=aggregate, text=True, env=env, capture_output=True)
                            expected = test_result == ('success' if selected == 'true' else 'skipped') and image_result == (
                                'success' if image_selected == 'true' else 'skipped')
                            self.assertEqual(result.returncode == 0, expected, (selected, test_result, image_selected, image_result))
        for override in ({'DEFERRED': 'true'}, {'PLAN_RESULT': 'failure'}, {'BACKEND': ''},
                         {'RUN_TESTS': ''}, {'BUILD_IMAGE': 'invalid'}, {'BACKEND': 'true'}):
            with tempfile.TemporaryDirectory() as directory:
                env = {**os.environ, 'PLAN_RESULT': 'success', 'DEFERRED': 'false',
                       'RUN_TESTS': 'false', 'REQUIRED_TESTS': 'false', 'TEST_RESULT': 'skipped', 'BUILD_IMAGE': 'false',
                       'DRAFT_EVENT': 'false', 'REQUIRED_BACKEND': 'false',
                       'REQUIRED_FRONTEND': 'false', 'REQUIRED_PYTHON': 'false',
                       'REUSED_BACKEND': 'false', 'REUSED_FRONTEND': 'false', 'REUSED_PYTHON': 'false',
                       'IMAGE_RESULT': 'skipped', 'BACKEND': 'false', 'FRONTEND': 'false', 'PYTHON': 'false',
                       'PACKAGING_REUSED': 'false', 'EXECUTE_IMAGE': 'false',
                       'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary'), **override}
                env.update(BACKEND_RESULT='skipped', BACKEND_GENERATE_RESULT='skipped', BACKEND_COMPILE_RESULT='skipped',
                           FRONTEND_RESULT='skipped', FRONTEND_GENERATE_RESULT='skipped', FRONTEND_COMPILE_RESULT='skipped',
                           PYTHON_RESULT='skipped')
                result = subprocess.run(['bash'], input=aggregate, text=True, env=env, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
        for job in jobs.values():
            for step in job["steps"]:
                if "run" in step:
                    subprocess.run(["bash", "-n"], input=step["run"], text=True, check=True, capture_output=True)


if __name__ == "__main__":
    unittest.main()
