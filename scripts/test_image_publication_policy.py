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
        result = self.plan(name="workflow_dispatch", ref="feature", event={
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

    def test_relevance_retains_validation_and_avoids_irrelevant_images(self):
        for paths in (["docs/DEVELOPMENT.md"], ["AGENTS.md", "REVIEW.md"], ["scripts/generate_release_notes.go"],
                      ["scripts/vrlog-server.mjs"], [".github/ISSUE_TEMPLATE/bug_report.yml"],
                      ["scripts/image_publication_policy.py", "scripts/test_image_publication_policy.py"],
                      ["ui/v2.5/README.md", "pkg/sqlite/migrations/README.md"],
                      ["docker/production/README.md", "docker/production/.env.example"]):
            self.assertEqual(policy.affected(paths), (False, False))
        for paths in (["pkg/ffmpeg/options_test.go"], ["cmd/gpu-generation-lab/main.go"],
                      ["ui/v2.5/src/components/Settings/SettingsSystemPanel.generation.test.tsx"],
                      ["scripts/gpu_generation/benchmark.py"], ["scripts/test_check_intel_runtime.py"]):
            self.assertEqual(policy.affected(paths), (True, False))
        for path in ("pkg/ffmpeg/options.go", "internal/api/server.go", "ui/v2.5/src/App.tsx",
                     "ui/v2.5/pnpm-lock.yaml", "graphql/schema/config.graphql", "Makefile", ".dockerignore",
                     "ui/v2.5/src/docs/en/Manual/Help.md", "scripts/generateLoginLocales.go",
                     "scripts/getDate.go", "scripts/check_intel_runtime.py", "docker/build/x86_64/Dockerfile"):
            self.assertEqual(policy.affected([path]), (True, True), path)

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
        self.assertEqual(policy.affected(paths), (True, True))
        with self.assertRaises(ValueError):
            policy.changed_paths("push", {"before": "--option", "after": SHA}, Path.cwd())

    def test_cli_requires_complete_gate_and_outputs_safe_values(self):
        with tempfile.TemporaryDirectory() as directory:
            event, output = Path(directory) / "event.json", Path(directory) / "output"
            event.write_text(json.dumps({"inputs": {"publish_test_image": "true", "test_label": "candidate"}}))
            env = {**os.environ, "GITHUB_EVENT_PATH": str(event), "GITHUB_OUTPUT": str(output),
                   "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF_TYPE": "branch",
                   "GITHUB_REF_NAME": "master", "GITHUB_SHA": SHA, "GITHUB_REPOSITORY": REPO,
                   "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1"}
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
        self.assertEqual(publisher["needs"], ["plan", "image"])
        self.assertIn("github.ref_type == 'tag'", publisher["if"])
        self.assertIn("inputs.publish_test_image", publisher["if"])
        self.assertFalse(any("checkout" in step.get("uses", "") for step in publisher["steps"]))
        for job in jobs.values():
            for step in job["steps"]:
                if "run" in step:
                    subprocess.run(["bash", "-n"], input=step["run"], text=True, check=True, capture_output=True)


if __name__ == "__main__":
    unittest.main()
