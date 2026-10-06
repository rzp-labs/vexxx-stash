"""Check symbol-build command generation without building or uploading."""
import os
from pathlib import Path
import subprocess
import unittest


class PostHogBuildTests(unittest.TestCase):
    def test_symbol_targets_pass_an_output_flag_to_go(self):
        root = Path(__file__).resolve().parent.parent
        for target in ("build-release-posthog-linux", "build-release-posthog-macos"):
            with self.subTest(target=target):
                result = subprocess.run(
                    ["make", "-n", target, "POSTHOG_BINARY=/tmp/posthog-test/stash",
                     "BUILD_DATE=test", "GITHASH=test", "STASH_VERSION=test"],
                    cwd=root, text=True, capture_output=True,
                    env={**os.environ, "POSTHOG_UPLOAD_REQUIRED": "false",
                         "POSTHOG_CLI_API_KEY": "", "POSTHOG_API_KEY": "",
                         "POSTHOG_PROJECT_TOKEN": "", "POSTHOG_HOST": ""},
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("go build -o /tmp/posthog-test/stash -v", result.stdout)
                self.assertNotIn("go build /tmp/posthog-test/stash", result.stdout)

    def test_docker_validation_binary_uses_an_explicit_output_flag(self):
        dockerfile = Path(__file__).resolve().parent.parent / "docker/build/x86_64/Dockerfile"
        self.assertIn('STASH_OUTPUT="-o dist/posthog/stash" stash', dockerfile.read_text())

    def test_backend_embeds_the_same_public_inputs_as_the_ui(self):
        root = Path(__file__).resolve().parent.parent
        dockerfile = (root / "docker/build/x86_64/Dockerfile").read_text()
        frontend = dockerfile.split("AS frontend", 1)[1].split("# Build Backend", 1)[0]
        backend = dockerfile.split("AS backend", 1)[1].split("# Final Runnable Image", 1)[0]
        for stage in (frontend, backend):
            for name in ("VITE_PUBLIC_POSTHOG_PROJECT_TOKEN", "VITE_PUBLIC_POSTHOG_HOST"):
                self.assertIn("ARG " + name + "\n", stage)
                self.assertIn(name + "=$" + name, stage)
        self.assertIn("COPY --from=backend /stash/dist/posthog/stash /usr/bin/", dockerfile)
        result = subprocess.run(
            ["make", "-n", "stash", "BUILD_DATE=test", "GITHASH=test", "STASH_VERSION=0.1.0",
             "VITE_PUBLIC_POSTHOG_PROJECT_TOKEN=phc_synthetic_browser_project",
             "VITE_PUBLIC_POSTHOG_HOST=https://us.i.posthog.com"],
            cwd=root, text=True, capture_output=True,
            env={**os.environ, "POSTHOG_PROJECT_TOKEN": "different-runtime-project",
                 "POSTHOG_HOST": "https://runtime.example", "POSTHOG_CLI_API_KEY": "upload-key-must-not-be-bundled"},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("internal/analytics.bundledProjectToken=phc_synthetic_browser_project", result.stdout)
        self.assertIn("internal/analytics.bundledHost=https://us.i.posthog.com", result.stdout)
        self.assertNotIn("different-runtime-project", result.stdout)
        self.assertNotIn("https://runtime.example", result.stdout)
        self.assertNotIn("upload-key-must-not-be-bundled", result.stdout)


if __name__ == "__main__":
    unittest.main()
