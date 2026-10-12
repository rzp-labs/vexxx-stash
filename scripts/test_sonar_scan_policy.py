import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import image_publication_policy as publication
from sonar_scan_policy import properties, scanner_plan, verify_source_snapshot, prepare_source_reports

ROOT = Path(__file__).resolve().parent.parent
REPO = 'rzp-labs/vexxx-stash'
SHA = 'a' * 40
CONFIG = properties(ROOT)


def pr(base='master', draft=False, head_repo=REPO):
    return {'pull_request': {'draft': draft,
                            'head': {'sha': SHA, 'repo': {'full_name': head_repo}},
                            'base': {'ref': base, 'repo': {'full_name': REPO}}}}


class ScanPolicyTests(unittest.TestCase):
    def selection(self, paths, name='pull_request', event=None, enabled=True):
        existing = publication.obligations(paths)
        groups, scan = scanner_plan(name, 'branch', 'master', REPO, event or pr(),
                                    paths, existing, CONFIG, enabled=enabled)
        self.assertEqual(groups['build_image'], existing['build_image'])
        self.assertEqual(groups['python'], existing['python'])
        return existing, groups, scan

    def test_disabled_preserves_every_existing_domain(self):
        for paths in (['README.md'], ['pkg/cache/lru.go'], ['ui/v2.5/src/App.tsx'],
                      ['scripts/reuse_master_validation.py'], ['.github/workflows/docker-publish.yml'],
                      ['docker/build/x86_64/Dockerfile']):
            with self.subTest(paths=paths):
                old, new, scan = self.selection(paths, name='push', enabled=False)
                self.assertEqual(old, new)
                self.assertFalse(scan['sonar_scan'])

    def test_pull_requests_reuse_selected_domains_without_expansion(self):
        for paths, status in ((['README.md'], 'import_no_coverage'),
                              (['pkg/cache/lru.go'], 'import_backend'),
                              (['ui/v2.5/src/App.tsx'], 'import_frontend'),
                              (['scripts/reuse_master_validation.py'], 'import_no_coverage'),
                              (['.github/workflows/docker-publish.yml'], 'import_backend_and_frontend')):
            with self.subTest(paths=paths):
                old, new, scan = self.selection(paths)
                self.assertEqual(old, new)
                self.assertTrue(scan['sonar_scan'])
                self.assertEqual(scan['sonar_status'], status)
                self.assertEqual(scan['sonar_added_domains'], '')
                self.assertEqual(bool(scan['sonar_go_paths']), new['backend'])
                self.assertEqual(bool(scan['sonar_lcov_paths']), new['frontend'])

    def test_master_documentation_and_unanalyzed_ci_inputs_add_no_work(self):
        for paths in (['README.md'], ['docs/DEVELOPMENT.md'],
                      ['ui/v2.5/README.md'], ['.codex/config.toml']):
            with self.subTest(paths=paths):
                old, new, scan = self.selection(paths, name='push')
                self.assertEqual(old, new)
                self.assertFalse(scan['sonar_scan'])
                self.assertEqual(scan['sonar_added_domains'], '')

    def test_master_adds_only_missing_domains_for_complete_snapshots(self):
        for paths, added in ((['pkg/cache/lru.go'], 'frontend'),
                             (['ui/v2.5/src/App.tsx'], 'backend'),
                             (['scripts/reuse_master_validation.py'], 'backend,frontend'),
                             (['.github/workflows/docker-publish.yml'], ''),
                             (['sonar-project.properties'], '')):
            with self.subTest(paths=paths):
                old, new, scan = self.selection(paths, name='push')
                self.assertEqual(new, old)
                self.assertTrue(scan['sonar_scan'])
                for domain in ('backend', 'frontend'):
                    self.assertEqual(scan['sonar_coverage_' + domain], not old[domain])
                self.assertEqual(scan['sonar_added_domains'], added)
                self.assertFalse(new['build_image'])

    def test_forks_missing_targets_and_drafts_never_receive_scanner_credentials(self):
        for event, status in ((pr(head_repo='other/fork'), 'fork_or_missing_trust_metadata'),
                              (pr(base=''), 'missing_target_branch'),
                              (pr(draft=True), 'draft'),
                              ({'pull_request': {}}, 'fork_or_missing_trust_metadata')):
            with self.subTest(event=event):
                old, new, scan = self.selection(['pkg/cache/lru.go'], event=event)
                self.assertEqual(old, new)
                self.assertFalse(scan['sonar_scan'])
                self.assertEqual(scan['sonar_status'], status)

    def test_same_repo_stacks_scan_actual_target_without_new_validation(self):
        event = pr(base='codex/stack-base')
        old, new, policy = self.selection(['pkg/cache/lru.go'], event=event)
        self.assertEqual(new, old)
        self.assertTrue(policy['sonar_scan'])
        enabled = publication.plan('pull_request', 'branch', 'stack-child', SHA, REPO,
                                   event, ['pkg/cache/lru.go'], declared_version='0.2.7', sonar_enabled=True)
        self.assertTrue(enabled['sonar_scan'])
        self.assertEqual(event['pull_request']['base']['ref'], 'codex/stack-base')

    def test_tags_dispatch_nonmaster_and_deleted_events_do_not_scan(self):
        groups = publication.obligations(['pkg/cache/lru.go'])
        for name, kind, ref, event in (('push', 'tag', 'v0.2.7', {}),
                                      ('workflow_dispatch', 'branch', 'master', {}),
                                      ('push', 'branch', 'feature', {}),
                                      ('push', 'branch', 'master', {'deleted': True})):
            with self.subTest(name=name, kind=kind, ref=ref):
                new, scan = scanner_plan(name, kind, ref, REPO, event,
                                         ['pkg/cache/lru.go'], groups, CONFIG, True)
                self.assertEqual(new, groups)
                self.assertFalse(scan['sonar_scan'])

    def test_enabled_plan_recomputes_obligations_without_publication_or_images(self):
        p = publication.plan('push', 'branch', 'master', SHA, REPO, {},
                             ['pkg/cache/lru.go'], declared_version='0.2.7', sonar_enabled=True)
        self.assertTrue(p['backend'] and p['run_tests'])
        self.assertFalse(p['frontend'])
        self.assertTrue(p['sonar_coverage_frontend'])
        self.assertFalse(p['sonar_coverage_backend'])
        self.assertTrue(p['sonar_scan'])
        self.assertFalse(p['publish'] or p['build_image'])

    def test_same_head_change_to_untrusted_scope_fails_required_validation(self):
        original = publication.plan('pull_request', 'branch', 'docs', SHA, REPO,
                                    pr(), ['README.md'], declared_version='0.2.7', sonar_enabled=True)
        self.assertTrue(original['sonar_scan'])
        retargeted = pr(head_repo='other/fork')
        self.assertEqual(retargeted['pull_request']['head']['sha'], SHA)
        with self.assertRaisesRegex(ValueError, 'Unsupported non-draft Sonar PR scope'):
            publication.plan('pull_request', 'branch', 'docs', SHA, REPO,
                             retargeted, ['README.md'], declared_version='0.2.7', sonar_enabled=True)
        # Baseline selection remains available for comparison in offline tests.
        disabled = publication.plan('pull_request', 'branch', 'docs', SHA, REPO,
                                    retargeted, ['README.md'], declared_version='0.2.7')
        self.assertFalse(disabled['sonar_scan'])
        import yaml
        workflow = yaml.safe_load((ROOT / '.github/workflows/docker-publish.yml').read_text())
        self.assertIn('edited', workflow['on']['pull_request']['types'])
        gate = next(s for s in workflow['jobs']['ci-required']['steps'] if s.get('id') == 'required_gate')
        self.assertIn("steps.plan.outcome == 'success'", gate['env']['PLAN_RESULT'])
        self.assertIn("if e['PLAN_RESULT'] != 'success':", gate['run'])

    def test_workflow_keeps_token_step_scoped_and_uses_exact_source_checkout(self):
        import yaml
        w = yaml.safe_load((ROOT / '.github/workflows/docker-publish.yml').read_text())
        self.assertNotIn('pull_request_target', w['on'])
        self.assertNotIn('SONAR_TOKEN', w.get('env', {}))
        self.assertNotIn('SONAR_TOKEN', w['jobs']['ci-required'].get('env', {}))
        steps = w['jobs']['ci-required']['steps']
        scan = next(x for x in steps if x.get('name') == 'Submit analysis to the existing Sonar project')
        self.assertEqual(scan['env']['SONAR_TOKEN'], '${{ secrets.SONAR_TOKEN }}')
        self.assertNotIn('continue-on-error', scan)
        self.assertEqual(sum(x.get('with', {}).get('path') == '~/.sonar/cache' for x in steps), 1)
        self.assertNotIn("base.ref == 'master'", scan['if'])
        self.assertEqual(scan['with']['projectBaseDir'], '.sonar-source')
        self.assertLess(next(i for i,x in enumerate(steps) if x.get('id') == 'required_gate'), steps.index(scan))
        self.assertEqual(w['jobs']['ci-required']['name'],
                         "${{ github.event_name == 'pull_request' && github.event.pull_request.draft && 'validation-deferred' || 'ci-required' }}")
        self.assertEqual(sum('go test -count=1' in x.get('run', '') for x in steps), 2)
        self.assertEqual(sum('pnpm run test:coverage' in x.get('run', '') for x in steps), 1)
        self.assertEqual(sum('pnpm exec vitest run --coverage' in x.get('run', '') for x in steps), 1)
        for domain in ('backend', 'frontend'):
            coverage = next(x for x in steps if x.get('id') == domain + '_coverage_test')
            self.assertNotIn('pnpm run check', coverage['run'])
            self.assertNotIn('node --test', coverage['run'])
            self.assertNotIn('pnpm run test:coverage', coverage['run'])
            self.assertNotIn('make flags', coverage['run'])
        for paths in (['README.md'], ['pkg/cache/lru.go'], ['ui/v2.5/src/App.tsx'],
                      ['scripts/reuse_master_validation.py'], ['.github/workflows/docker-publish.yml']):
            for name in ('push', 'pull_request'):
                old, new, policy = self.selection(paths, name=name)
                for domain in ('backend', 'frontend'):
                    self.assertFalse(new[domain] and policy['sonar_coverage_' + domain])
        for compile_id in ('backend_compile', 'frontend_compile'):
            compile_step = next(x for x in steps if x.get('id') == compile_id)
            self.assertNotIn('sonar_coverage', compile_step['if'])


class SourceIdentityTests(unittest.TestCase):
    def test_static_scan_allows_reachable_python_only_merge_drift(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)

            def git(*args):
                return subprocess.check_output(['git', '-c', 'user.name=nehpz',
                                                '-c', 'user.email=admin@rzp.one',
                                                *args], cwd=root, text=True).strip()

            git('init', '-q', '-b', 'master')
            (root / 'scripts').mkdir()
            script = root / 'scripts/reuse_master_validation.py'
            script.write_text('INITIAL = True\n')
            (root / 'sonar-project.properties').write_text((ROOT / 'sonar-project.properties').read_text())
            git('add', 'scripts/reuse_master_validation.py', 'sonar-project.properties')
            git('commit', '-qm', 'Fixture baseline')
            git('checkout', '-qb', 'docs')
            (root / 'README.md').write_text('Documentation only\n')
            git('add', 'README.md')
            git('commit', '-qm', 'Fixture docs PR')
            head = git('rev-parse', 'HEAD')
            git('checkout', '-q', 'master')
            script.write_text('INITIAL = False\n')
            git('add', 'scripts/reuse_master_validation.py')
            git('commit', '-qm', 'Fixture Python-only master advance')
            base = git('rev-parse', 'HEAD')
            git('merge', '--no-ff', '-qm', 'Fixture tested merge', 'docs')
            tested = git('rev-parse', 'HEAD')
            event = pr()
            event['pull_request']['head']['sha'] = head
            event['pull_request']['base']['sha'] = base
            paths = publication.changed_paths('pull_request', event, root, tested)
            self.assertEqual(set(paths), {'README.md', 'scripts/reuse_master_validation.py'})
            selected = publication.plan('pull_request', 'branch', 'docs', tested, REPO,
                                        event, paths, declared_version='0.2.7', sonar_enabled=True)
            self.assertTrue(selected['python'] and selected['sonar_scan'])
            self.assertFalse(selected['backend'] or selected['frontend'])
            self.assertEqual(selected['sonar_go_paths'], '')
            self.assertEqual(selected['sonar_lcov_paths'], '')
            source = root / '.sonar-source'
            git('clone', '-q', '--shared', '.', str(source))
            subprocess.run(['git', 'checkout', '-q', '--detach', head], cwd=source, check=True)
            with self.assertRaisesRegex(ValueError, 'inputs differ'):
                verify_source_snapshot(root, tested, head, CONFIG)
            prepare_source_reports(root, source, tested, head, '', '')

    def test_static_scan_still_rejects_wrong_analysis_checkout(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / 'sonar-project.properties').write_text((ROOT / 'sonar-project.properties').read_text())
            (root / '.sonar-source').mkdir()
            with patch('sonar_scan_policy.verify_source_snapshot') as compare, patch('sonar_scan_policy.subprocess.check_output', return_value='b' * 40 + '\n'):
                with self.assertRaisesRegex(ValueError, 'checkout does not match'):
                    prepare_source_reports(root, root / '.sonar-source', 'b' * 40, SHA, '', '')
                compare.assert_not_called()

    def test_real_git_rejects_source_drift_but_allows_documentation_drift(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)

            def git(*args):
                return subprocess.check_output(['git', '-c', 'user.name=nehpz',
                                                '-c', 'user.email=admin@rzp.one',
                                                *args], cwd=root, text=True).strip()

            git('init', '-q')
            (root / 'pkg').mkdir()
            (root / 'pkg/helper.go').write_text('package pkg\n')
            git('add', 'pkg/helper.go')
            git('commit', '-qm', 'Fixture source')
            original = git('rev-parse', 'HEAD')
            (root / 'README.md').write_text('Documentation only\n')
            git('add', 'README.md')
            git('commit', '-qm', 'Fixture documentation')
            verify_source_snapshot(root, original, git('rev-parse', 'HEAD'), CONFIG)
            (root / 'pkg/helper.go').write_text('package pkg\nvar Changed = true\n')
            git('add', 'pkg/helper.go')
            git('commit', '-qm', 'Fixture source drift')
            with self.assertRaisesRegex(ValueError, 'inputs differ'):
                verify_source_snapshot(root, original, git('rev-parse', 'HEAD'), CONFIG)

    def test_snapshot_comparison_includes_sources_tests_and_build_inputs(self):
        with patch('sonar_scan_policy.subprocess.run') as run:
            run.return_value.returncode = 0
            verify_source_snapshot(ROOT, 'a' * 40, 'b' * 40, CONFIG)
            args = run.call_args.args[0]
            self.assertEqual(args[:5], ['git', 'diff', '--quiet', 'a' * 40, 'b' * 40])
            self.assertIn('pkg', args)
            self.assertIn('ui/v2.5', args)
            self.assertIn('go.sum', args)
            self.assertIn('sonar-project.properties', args)
            run.return_value.returncode = 1
            with self.assertRaisesRegex(ValueError, 'inputs differ'):
                verify_source_snapshot(ROOT, 'a' * 40, 'b' * 40, CONFIG)

    def test_invalid_revision_is_rejected_before_running_git(self):
        with patch('sonar_scan_policy.subprocess.run') as run:
            with self.assertRaisesRegex(ValueError, 'invalid source revision'):
                verify_source_snapshot(ROOT, 'master;anything', SHA, CONFIG)
            run.assert_not_called()

    def test_available_derived_types_reused_only_after_report_input_verification(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / 'sonar-project.properties').write_text((ROOT / 'sonar-project.properties').read_text())
            source = root / '.sonar-source'
            (source / 'ui/v2.5').mkdir(parents=True)
            deps = root / 'ui/v2.5/node_modules'
            deps.mkdir(parents=True)
            generated = root / 'ui/v2.5/src/core/generated-graphql.ts'
            generated.parent.mkdir(parents=True)
            generated.write_text('export type FixtureQuery = string;\n')
            report = root / CONFIG['sonar.go.coverage.reportPaths'].split(',')[0]
            report.parent.mkdir(parents=True)
            report.write_text('Test fixture for copy boundary\n')
            with patch('sonar_scan_policy.verify_source_snapshot') as compare, patch('sonar_scan_policy.subprocess.check_output', return_value=SHA + '\n'):
                prepare_source_reports(root, source, SHA, SHA, '', '')
                self.assertFalse((source / 'ui/v2.5/node_modules').exists())
                compare.assert_not_called()
                prepare_source_reports(root, source, SHA, SHA, report.relative_to(root).as_posix(), '')
                compare.assert_called_once()
                self.assertEqual((source / 'ui/v2.5/node_modules').resolve(), deps.resolve())
                self.assertEqual((source / 'ui/v2.5/src/core/generated-graphql.ts').read_text(), generated.read_text())

    def test_missing_selected_report_does_not_become_fake_coverage(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / 'sonar-project.properties').write_text((ROOT / 'sonar-project.properties').read_text())
            (root / '.sonar-source').mkdir()
            with patch('sonar_scan_policy.verify_source_snapshot'), patch('sonar_scan_policy.subprocess.check_output', return_value=SHA + '\n'):
                with self.assertRaisesRegex(ValueError, 'report missing or empty'):
                    prepare_source_reports(root, root / '.sonar-source', SHA, SHA,
                                           CONFIG['sonar.go.coverage.reportPaths'], '')


if __name__ == '__main__':
    unittest.main()
