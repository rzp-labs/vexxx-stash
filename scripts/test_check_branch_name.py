import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_branch_name

REPO = "rzp-labs/vexxx-stash"


class CheckBranchNameTests(unittest.TestCase):
    def source_pr(self, branch='codex/vex-117', repository=REPO, **kwargs):
        return {'head': {'ref': branch, 'repo': {'full_name': repository}}, **kwargs}

    def test_issue_source_names_accept_case_and_hyphenated_suffixes_from_forks(self):
        for repository in (REPO, 'contributor/vexxx-stash'):
            for branch in ('codex/vex-117', 'nehpz/VEX-117-branch-rules',
                           'codex/vex-111-wrapper-20261012', 'A-B/vEx-1-Suffix-2'):
                with self.subTest(repository=repository, branch=branch):
                    check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch, repository))

    def test_issue_source_names_reject_invalid_and_injection_shaped_names(self):
        for branch in ('vex-117', 'codex/117', 'codex/vex-0', 'codex/vex-0117',
                       'codex/ct-117', 'codex/vex-117-', 'codex/vex-117--suffix',
                       'codex/vex-117/path', '-codex/vex-117', 'codex-/vex-117',
                       'co--dex/vex-117', 'co_dex/vex-117', 'codex/vex-117_under',
                       'codex/vex-117\n', 'codex/vex-117;echo-secret',
                       'codex/$(echo-secret)', 'codex/`echo-secret`', 'codex/vex-١١٧'):
            with self.subTest(branch=branch), self.assertRaisesRegex(ValueError, 'PR source branch must use'):
                check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch))

    def test_source_exceptions_are_exact_and_repository_owned(self):
        accepted = ('master', 'codex/ct102-media-generation', 'feat/posthog-instrumentation',
                    'release-v0.2.7', 'release-v0.0.0', 'release-v12.345.678')
        for branch in accepted:
            with self.subTest(branch=branch):
                check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch))
                with self.assertRaises(ValueError):
                    check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch, 'fork/vexxx-stash'))
        for branch in ('Master', 'codex/ct102-media-generation-extra', 'feat/posthog-other',
                       'release-v1.2', 'release-v01.2.3', 'release-v1.02.3', 'release-v1.2.03',
                       'release-v1.2.3-rc.1', 'release-v1.2.3+build', 'release-v1.2.3/extra',
                       'release-v1.2.3\n', 'Release-v1.2.3'):
            with self.subTest(branch=branch), self.assertRaises(ValueError):
                check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch))

    def test_dependabot_requires_same_repository_and_original_verified_bot_author(self):
        bot = {'login': 'dependabot[bot]', 'type': 'Bot'}
        for branch in ('dependabot/library', 'dependabot/npm_and_yarn/ui/v2.5/library-1.2.3'):
            check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch, user=bot))
            for repository, author in ((REPO, None), (REPO, {}), (REPO, 'dependabot[bot]'),
                                       (REPO, {'login': 'dependabot[bot]', 'type': 'User'}),
                                       (REPO, {'login': 'other[bot]', 'type': 'Bot'}),
                                       ('fork/vexxx-stash', bot)):
                with self.subTest(branch=branch, repository=repository, author=author):
                    with self.assertRaises(ValueError):
                        check_branch_name.validate_pr_source_branch(
                            REPO, self.source_pr(branch, repository, user=author))
        for branch in ('dependabot', 'dependabot/', 'Dependabot/library', 'human/feature'):
            with self.assertRaises(ValueError):
                check_branch_name.validate_pr_source_branch(REPO, self.source_pr(branch, user=bot))
        # The helper never consults event.sender or GITHUB_ACTOR on a rerun.
        with patch.dict(os.environ, {'GITHUB_ACTOR': 'dependabot[bot]'}), self.assertRaises(ValueError):
            check_branch_name.validate_pr_source_branch(REPO, self.source_pr('dependabot/library', sender=bot))

    def test_source_metadata_fails_closed_even_for_conforming_names(self):
        for pr in (None, [], {}, {'head': None}, {'head': []}, {'head': {}},
                   {'head': {'ref': 'codex/vex-117'}}, self.source_pr(None), self.source_pr(''),
                   self.source_pr(117), self.source_pr(repository=None), self.source_pr(repository=''),
                   self.source_pr(repository='repo'), self.source_pr(repository='owner/repo\n'),
                   {'head': {'ref': 'codex/vex-117', 'repo': None}},
                   {'head': {'ref': 'codex/vex-117', 'repo': {'full_name': 117}}}):
            with self.subTest(pr=pr), self.assertRaisesRegex(ValueError, 'missing or malformed'):
                check_branch_name.validate_pr_source_branch(REPO, pr)
        check_branch_name.validate_pr_source_branch('other/repository', None)


if __name__ == '__main__':
    unittest.main()
