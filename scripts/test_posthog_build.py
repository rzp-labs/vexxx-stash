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


if __name__ == "__main__":
    unittest.main()
