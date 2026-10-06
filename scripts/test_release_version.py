import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from release_version import development_version, read_version, validate_version


class ReleaseVersionTests(unittest.TestCase):
    def test_stable_semver_and_strict_file(self):
        for value in ('0.1.0', '1.2.3', '12.345.678'):
            self.assertEqual(validate_version(value), value)
        for value in ('v0.1.0', '01.2.3', '1.02.3', '1.2.03', '1.2', '1.2.3-rc.1',
                      'feature-name', '1.2.3\n', '', None, '9' * 121 + '.0.0'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_version(value)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'VERSION'
            with self.assertRaises(FileNotFoundError):
                read_version(path)
            path.write_text('0.1.0\n')
            self.assertEqual(read_version(path), '0.1.0')
            for contents in ('0.1.0\n\n', ' 0.1.0\n', '0.1.0\n1.0.0\n'):
                path.write_text(contents)
                with self.assertRaises(ValueError):
                    read_version(path)

    def test_development_is_semver_with_separate_revision(self):
        self.assertEqual(development_version('0.1.0', 'a' * 40), '0.1.0-dev+sha.aaaaaaaaaaaa')
        for revision in ('feature-name', 'a' * 12, 'A' * 40, 'a' * 40 + '\n'):
            with self.assertRaises(ValueError):
                development_version('0.1.0', revision)

    def test_make_defaults_do_not_inherit_upstream_tags_and_explicit_release_matches(self):
        root = Path(__file__).resolve().parent.parent
        revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
        command = ['make', '-s', '-f', 'Makefile', '-f', '-', 'print-version', 'BUILD_DATE=test']
        target = 'print-version: build-info\n\t@echo $(STASH_VERSION) $(GITHASH) $(STASH_RELEASE_REPO)\n'
        env = {key: value for key, value in os.environ.items()
               if key not in ('STASH_VERSION', 'GITHASH', 'STASH_RELEASE_REPO')}
        output = subprocess.check_output(command, input=target, cwd=root, env=env, text=True).split()
        self.assertEqual(output[0], development_version(read_version(), revision))
        self.assertEqual(output[1], revision[:len(output[1])])
        self.assertEqual(output[2], 'rzp-labs/vexxx-stash')
        output = subprocess.check_output(command + ['STASH_VERSION=0.1.0', 'GITHASH=' + revision],
                                         input=target, cwd=root, env=env, text=True).split()
        self.assertEqual(output[:2], ['0.1.0', revision])
