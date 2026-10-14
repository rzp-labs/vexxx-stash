import base64
import copy
import io
import json
import os
from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import MagicMock, patch
import zipfile

import reuse_sonar_validation as reuse

REPO = 'rzp-labs/vexxx-stash'
H, M, B, T = ('a' * 40, 'b' * 40, 'c' * 40, 'd' * 40)
OLD_H, OLD_M, OLD_T = ('e' * 40, 'f' * 40, '1' * 40)
WORKFLOW = b'fixed workflow bytes'
PROPERTIES = b'sonar.projectKey=project\nsonar.exclusions=assets/*.png\n'
START, SCAN, END = ('2026-10-12T01:00:00Z', '2026-10-12T01:05:00Z', '2026-10-12T01:10:00Z')
VARIABLES = {key: '' for key in reuse.VARIABLES}
ENTRIES = [dict(path='app.go', mode='100644', type='blob', sha='2' * 40),
           dict(path=reuse.WORKFLOW, mode='100644', type='blob', sha='3' * 40),
           dict(path='sonar-project.properties', mode='100644', type='blob', sha='4' * 40)]
INPUTS = reuse.fingerprint(ENTRIES, VARIABLES)
PLAN = dict(checkout=M, head=H, base=B, tree=T, pr=65, target='master', action='synchronize', build_image=False,
            sonar_go_paths='dist/coverage/go.out',
            sonar_lcov_paths='dist/coverage/frontend/lcov.info',
            sonar_python_paths='dist/coverage/python.xml')
IDENTITY = dict(head=H, base=B, tree=T, merge=M, target='master')


def archive(files):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, 'w') as output:
        for name, value in files.items():
            output.writestr(name, value)
    return stream.getvalue()


def check():
    return dict(id=999, name='SonarCloud Code Analysis', app={'id': 12526}, head_sha=H,
                status='completed', conclusion='success', pull_requests=[{'number': 65}],
                details_url='https://sonarcloud.io/dashboard?id=rzp-labs_vexxx-stash&pullRequest=65',
                started_at=SCAN, completed_at=END)


class Fixture:
    def __init__(self, old=False, inherited=False):
        self.repo = REPO
        self.head, self.merge, self.tree = (OLD_H, OLD_M, OLD_T) if old else (H, M, T)
        self.run = dict(id=123 if old else 124, run_attempt=2, event='pull_request',
                        pull_requests=[{'number': 65}], head_sha=self.head, path=reuse.WORKFLOW,
                        workflow_id=45, repository={'full_name': REPO},
                        head_repository={'full_name': REPO}, status='completed', conclusion='success')
        names = ['Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
                 reuse.REUSE_STEP, 'Verify every selected obligation', 'Record executed native validation',
                 'Save executed validation evidence', 'Submit analysis to the existing Sonar project',
                 'Record current native Sonar clearance', 'Save current Sonar clearance',
                 *reuse.RETAIN.values()]
        names += [name for steps in reuse.DOMAINS.values() for name in steps]
        self.job = dict(name='ci-required', head_sha=self.head, run_id=self.run['id'], run_attempt=2,
                        status='completed', conclusion='success', started_at=START, completed_at=END,
                        steps=[dict(name=name, status='completed', conclusion='success') for name in names])
        self.proof = dict(schema=1, repository=REPO, run=self.run['id'], attempt=2, checkout=self.merge,
                          tree=self.tree, head=self.head, base=B, pr=65, input_digest=INPUTS,
                          workflow_digest=reuse.digest(WORKFLOW),
                          variables_digest=reuse.digest(reuse.canonical(VARIABLES)),
                          build_image=False, domains={d: not inherited for d in reuse.DOMAINS})
        if inherited:
            for step in self.job['steps']:
                if any(step['name'] in names for names in reuse.DOMAINS.values()):
                    step['conclusion'] = 'skipped'
        self.clearance = dict(schema=1, repository=REPO, run=self.run['id'], attempt=2, pr=65,
                              **IDENTITY, analyzed_revision=H, quality_gate='OK', ce_task_id='task-id',
                              scan_started_at=SCAN, sonar_check_id=999, check_completed_at=END,
                              workflow_digest=reuse.digest(WORKFLOW), config_digest=reuse.digest(PROPERTIES))
        self.files = {}
        self.artifacts = []
        self.commit_parents = [B, self.head]
        self.truncated_tree = False
        self.total_jobs = 1
        self.total_artifacts = None
        self.fresh_attempt = 2
        self.workflow = WORKFLOW
        self.properties = PROPERTIES.replace(b'assets/*.png', b'old/*.png') if old else PROPERTIES
        self.refresh()

    def refresh(self):
        self.files.clear()
        self.artifacts.clear()
        for name, files in (
            ('validation-evidence-2', {'validation-evidence.json': reuse.canonical(self.proof)}),
            ('sonar-clearance-2', {'sonar-clearance.json': reuse.canonical(self.clearance)}),
            ('go-coverage', {'go.out': b'mode: atomic\nfile.go:1.1,1.3 1 1\n'}),
            ('frontend-coverage', {'lcov.info': b'SF:ui/v2.5/src/app.ts\nDA:1,1\nend_of_record\n'}),
            ('python-coverage', {'python.xml': b'<coverage><sources><source></source></sources></coverage>\n'})):
            ident = self.run['id'] * 10 + len(self.artifacts)
            raw = archive(files)
            self.files[ident] = raw
            self.artifacts.append(dict(id=ident, name=name, expired=False,
                                       workflow_run={'id': self.run['id']}, created_at=SCAN,
                                       digest='sha256:' + reuse.digest(raw)))

    def api(self, path):
        prefix = f"/actions/runs/{self.run['id']}"
        if path == '/actions/workflows/docker-publish.yml':
            return {'id': 45, 'path': reuse.WORKFLOW}
        if '/runs?' in path:
            return {'workflow_runs': [self.run]}
        if path == prefix:
            return {**self.run, 'run_attempt': self.fresh_attempt}
        if path == prefix + '/attempts/2/jobs?per_page=100':
            return {'jobs': [self.job], 'total_count': self.total_jobs}
        if path == prefix + '/artifacts?per_page=100':
            return {'artifacts': self.artifacts,
                    'total_count': len(self.artifacts) if self.total_artifacts is None else self.total_artifacts}
        if path.startswith('/git/commits/'):
            revision = path.rsplit('/', 1)[-1]
            return {'sha': revision, 'tree': {'sha': self.tree},
                    'parents': [{'sha': p} for p in self.commit_parents]}
        if path.startswith('/git/trees/'):
            return {'sha': self.tree, 'truncated': self.truncated_tree, 'tree': ENTRIES}
        if path.startswith('/contents/'):
            raw = self.workflow if path.startswith('/contents/' + reuse.WORKFLOW + '?') else self.properties
            return {'encoding': 'base64', 'content': base64.b64encode(raw).decode()}
        if path.startswith('/commits/') and '/check-runs?' in path:
            return {'total_count': 1, 'check_runs': [check()]}
        raise AssertionError(path)

    def find(self, required=None, ready=False, **kwargs):
        return reuse.find_source(self.api, self.files.__getitem__, PLAN, IDENTITY, REPO, '1000',
                                 WORKFLOW, PROPERTIES, VARIABLES,
                                 set(reuse.DOMAINS) if required is None else required, INPUTS, ready, **kwargs)


class ArchiveTests(unittest.TestCase):
    def test_exact_digest_and_nonempty_inventory(self):
        raw = archive({'lcov.info': b'actual report'})
        self.assertEqual(reuse.unpack(raw, 'sha256:' + reuse.digest(raw), {'lcov.info'}),
                         {'lcov.info': b'actual report'})
        for files in ({'../lcov.info': b'escape'}, {'lcov.info': b''},
                      {'lcov.info': b'ok', 'extra': b'not allowed'}):
            raw = archive(files)
            with self.assertRaises(ValueError):
                reuse.unpack(raw, 'sha256:' + reuse.digest(raw), {'lcov.info'})
        with self.assertRaises(ValueError):
            reuse.unpack(archive({'lcov.info': b'actual'}), 'sha256:' + '0' * 64, {'lcov.info'})

    def test_duplicate_zip_inventory_and_expanded_limit(self):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, 'w') as output:
            output.writestr('same', b'one')
            with self.assertWarns(UserWarning):
                output.writestr('same', b'two')
        raw = stream.getvalue()
        with self.assertRaises(ValueError):
            reuse.unpack(raw, 'sha256:' + reuse.digest(raw), {'same'})
        raw = archive({'lcov.info': b'A' * 300})
        with self.assertRaises(ValueError):
            reuse.unpack(raw, 'sha256:' + reuse.digest(raw), {'lcov.info'}, 200)


class IdentityTests(unittest.TestCase):
    def test_fingerprint_binds_every_blob_mode_path_and_variables(self):
        for key, value in [('sha', '5' * 40), ('mode', '100755'), ('path', 'other.go')]:
            changed = copy.deepcopy(ENTRIES)
            changed[0][key] = value
            self.assertNotEqual(reuse.fingerprint(changed, VARIABLES), INPUTS)
        self.assertNotEqual(reuse.fingerprint(ENTRIES, {**VARIABLES, 'POSTHOG_CLI_HOST': 'changed'}), INPUTS)
        changed = copy.deepcopy(ENTRIES)
        changed[-1]['sha'] = '6' * 40
        self.assertEqual(reuse.fingerprint(changed, VARIABLES), INPUTS)

    def test_only_exclusions_may_differ(self):
        self.assertEqual(reuse.config(PROPERTIES), reuse.config(PROPERTIES.replace(b'assets/*.png', b'other')))
        with self.assertRaises(ValueError):
            reuse.config(PROPERTIES + b'sonar.projectKey=duplicate\n')
        self.assertNotEqual(reuse.config(PROPERTIES), reuse.config(PROPERTIES + b'sonar.sourceEncoding=ASCII\n'))

    def test_real_native_check_empty_association_binds_project_and_pr_link(self):
        actual = {**check(), 'id': 114939290276, 'pull_requests': []}
        self.assertTrue(reuse.native_check(actual, H, 65, SCAN))
        actual.pop('pull_requests')
        self.assertTrue(reuse.native_check(actual, H, 65, SCAN))
        self.assertFalse(reuse.native_check({**actual, 'head_sha': OLD_H}, H, 65, SCAN))
        self.assertFalse(reuse.native_check({**actual, 'app': {'id': 42}}, H, 65, SCAN))
        self.assertFalse(reuse.native_check({**actual, 'started_at': START}, H, 65, SCAN))

    def test_native_link_rejects_wrong_scheme_host_project_pr_and_duplicate_queries(self):
        link = check()['details_url']
        for wrong in (None, '', link.replace('https:', 'http:'),
                      link.replace('sonarcloud.io', 'sonarcloud.io.evil.test'),
                      link.replace('sonarcloud.io', 'user@sonarcloud.io'),
                      link.replace('sonarcloud.io', 'sonarcloud.io:443'),
                      link.replace('vexxx-stash', 'other'), link.replace('pullRequest=65', 'pullRequest=66'),
                      link + '&pullRequest=65', link + '&id=rzp-labs_vexxx-stash',
                      link + '&extra=x&extra=y', link + '#other', link.replace('/dashboard', '/other'),
                      link.replace('&pullRequest=65', '')):
            with self.subTest(link=wrong):
                self.assertFalse(reuse.native_check({**check(), 'details_url': wrong, 'pull_requests': []}, H, 65, SCAN))

    def test_native_check_rejects_old_wrong_app_revision_pr_and_gate(self):
        self.assertTrue(reuse.native_check(check(), H, 65, SCAN))
        for key, value in [('app', {'id': 42}), ('head_sha', OLD_H), ('pull_requests', [{'number': 66}]),
                           ('conclusion', 'failure'), ('status', 'in_progress'), ('started_at', START)]:
            self.assertFalse(reuse.native_check({**check(), key: value}, H, 65, SCAN))
        with self.assertRaises(ValueError):
            reuse.timestamp('2026-10-12T01:00:00')


class ReuseTests(unittest.TestCase):
    def test_native_donor_and_config_only_change_restore_real_reports(self):
        fixture = Fixture(old=True)
        source = fixture.find()
        self.assertEqual(source['covered'], set(reuse.DOMAINS))
        self.assertEqual(set(source['reports']), {'dist/coverage/go.out',
                         'dist/coverage/frontend/lcov.info',
                         'dist/coverage/python.xml'})
        self.assertEqual(source['check_id'], '')  # Other events never reuse scan clearance.

    def test_draft_scan_with_only_final_packaging_guard_failure_clears_readiness(self):
        fixture = Fixture()
        fixture.run['conclusion'] = fixture.job['conclusion'] = 'failure'
        fixture.job['steps'].append(dict(name='Require selected packaging validation',
                                         status='completed', conclusion='failure'))
        self.assertEqual(fixture.find(ready=True)['check_id'], 999)
        # A failure/cancellation outside the deliberately pending guard is fatal.
        for name in ('other failed step', 'Submit analysis to the existing Sonar project',
                     'Record current native Sonar clearance', 'Save current Sonar clearance'):
            for outcome in ('failure', 'cancelled'):
                changed = copy.deepcopy(fixture)
                matches = [s for s in changed.job['steps'] if s['name'] == name]
                if matches:
                    matches[0]['conclusion'] = outcome
                else:
                    changed.job['steps'].append(dict(name=name, status='completed', conclusion=outcome))
                self.assertIsNone(changed.find(ready=True))

    def image_fixture(self):
        fixture = Fixture()
        fixture.proof['build_image'] = True
        for domain in ('backend', 'frontend'):
            host = next(s for s in fixture.job['steps'] if s['name'] == reuse.DOMAINS[domain][-1])
            host['conclusion'] = 'skipped'
        fixture.job['steps'] += [dict(name=name, status='completed', conclusion='success')
                                for name in ('Build Linux amd64 image locally', 'Smoke test container')]
        fixture.refresh()
        return fixture

    def test_image_donor_compile_requires_actual_build_and_smoke_with_skipped_host(self):
        fixture = self.image_fixture()
        self.assertEqual(fixture.find()['covered'], set(reuse.DOMAINS))
        self.assertEqual(fixture.find(ready=True)['check_id'], 999)
        for name in ('Build Linux amd64 image locally', 'Smoke test container'):
            for outcome in ('failure', 'skipped', 'missing'):
                changed = copy.deepcopy(fixture)
                if outcome == 'missing':
                    changed.job['steps'] = [s for s in changed.job['steps'] if s['name'] != name]
                else:
                    next(s for s in changed.job['steps'] if s['name'] == name)['conclusion'] = outcome
                self.assertEqual(changed.find()['covered'], {'python'})
                self.assertIsNone(changed.find(ready=True))
        for domain in ('backend', 'frontend'):
            for outcome in ('success', 'failure', 'missing'):
                changed = copy.deepcopy(fixture)
                host_name = reuse.DOMAINS[domain][-1]
                if outcome == 'missing':
                    changed.job['steps'] = [s for s in changed.job['steps'] if s['name'] != host_name]
                else:
                    next(s for s in changed.job['steps'] if s['name'] == host_name)['conclusion'] = outcome
                self.assertNotIn(domain, changed.find()['covered'])

    def test_build_provenance_flag_requires_actual_boolean(self):
        for value in ('true', 1, None):
            fixture = self.image_fixture()
            fixture.proof['build_image'] = value
            fixture.refresh()
            self.assertIsNone(fixture.find())
        fixture = self.image_fixture()
        del fixture.proof['build_image']
        fixture.refresh()
        self.assertIsNone(fixture.find())

    def test_failed_sonar_donor_can_prove_native_tests_but_not_clearance(self):
        fixture = Fixture()
        fixture.run['conclusion'] = fixture.job['conclusion'] = 'failure'
        next(s for s in fixture.job['steps'] if s['name'] == 'Submit analysis to the existing Sonar project')['conclusion'] = 'failure'
        self.assertEqual(fixture.find()['covered'], set(reuse.DOMAINS))
        self.assertIsNone(fixture.find(ready=True))

    def test_required_native_generation_tests_compile_and_gate_fail_closed(self):
        for name in ['Verify every selected obligation', 'Record executed native validation'] + [
                name for steps in reuse.DOMAINS.values() for name in steps]:
            fixture = Fixture()
            next(s for s in fixture.job['steps'] if s['name'] == name)['conclusion'] = 'skipped'
            source = fixture.find()
            if name in ('Verify every selected obligation', 'Record executed native validation'):
                self.assertIsNone(source)
            else:
                domain = next(d for d, steps in reuse.DOMAINS.items() if name in steps)
                self.assertNotIn(domain, source['covered'])

    def test_trust_fresh_attempt_inventory_and_workflow_fail_closed(self):
        mutations = [lambda f: setattr(f, 'fresh_attempt', 3), lambda f: setattr(f, 'total_jobs', 2),
                     lambda f: setattr(f, 'total_artifacts', 99), lambda f: setattr(f, 'truncated_tree', True),
                     lambda f: setattr(f, 'workflow', b'different'),
                     lambda f: f.run.update(head_repository={'full_name': 'fork/repo'}),
                     lambda f: f.run.update(pull_requests=[{'number': 66}]),
                     lambda f: setattr(f, 'commit_parents', [H, B])]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                fixture = Fixture()
                mutate(fixture)
                self.assertIsNone(fixture.find())

    def test_forged_proof_tree_input_or_artifact_digest_rejected(self):
        for key, value in [('tree', OLD_T), ('input_digest', '0' * 64), ('workflow_digest', '0' * 64),
                           ('variables_digest', '0' * 64), ('head', OLD_H), ('base', OLD_H)]:
            fixture = Fixture()
            fixture.proof[key] = value
            fixture.refresh()
            self.assertIsNone(fixture.find())
        fixture = Fixture()
        fixture.artifacts[0]['digest'] = 'sha256:' + '0' * 64
        self.assertIsNone(fixture.find())

    def test_changed_real_donor_blob_rejected_despite_forged_artifact_claim(self):
        fixture = Fixture()
        api = fixture.api
        def changed(path):
            data = api(path)
            if path.startswith('/git/trees/'):
                data = copy.deepcopy(data)
                data['tree'][0]['sha'] = '5' * 40
            return data
        with patch.object(fixture, 'api', changed):
            self.assertIsNone(fixture.find())

    def test_stale_expired_duplicate_report_and_artifact_traversal_rejected(self):
        for mutate in [lambda f: f.artifacts[2].update(expired=True),
                       lambda f: f.artifacts[2].update(created_at='2026-10-11T01:00:00Z'),
                       lambda f: f.artifacts.append(copy.deepcopy(f.artifacts[2]))]:
            fixture = Fixture()
            mutate(fixture)
            self.assertIsNone(fixture.find())
        fixture = Fixture()
        raw = archive({'../go.out': b'mode: atomic'})
        artifact = fixture.artifacts[2]
        artifact['digest'] = 'sha256:' + reuse.digest(raw)
        fixture.files[artifact['id']] = raw
        self.assertIsNone(fixture.find())

    def test_exact_ready_clearance_and_changed_context_rejection(self):
        fixture = Fixture()
        self.assertEqual(fixture.find(ready=True)['check_id'], 999)
        for key, value in [('base', OLD_H), ('tree', OLD_T), ('target', 'stack'),
                           ('head', OLD_H), ('quality_gate', 'ERROR'), ('config_digest', '0' * 64),
                           ('analyzed_revision', OLD_H), ('sonar_check_id', 888)]:
            fixture = Fixture()
            fixture.clearance[key] = value
            fixture.refresh()
            self.assertIsNone(fixture.find(ready=True))

    def test_ready_can_use_original_native_donor_and_new_config_scan_separately(self):
        original, current = Fixture(old=True), Fixture(inherited=True)
        def api(path):
            if '/runs?' in path:
                return {'workflow_runs': [current.run, original.run]}
            if '/actions/runs/123' in path or path.startswith('/git/commits/' + OLD_H) or (
                    path.startswith('/git/commits/' + OLD_M)) or '/git/trees/' + OLD_T in path or (
                    '?ref=' + OLD_H in path or '?ref=' + OLD_M in path):
                return original.api(path)
            return current.api(path)
        files = {**original.files, **current.files}
        result = reuse.find_source(api, files.__getitem__, PLAN, IDENTITY, REPO, '1000', WORKFLOW,
                                   PROPERTIES, VARIABLES, set(reuse.DOMAINS), INPUTS, True)
        self.assertEqual(result['covered'], set(reuse.DOMAINS))
        self.assertEqual(result['run'], original.run['id'])
        self.assertEqual(result['check_id'], 999)

    def test_static_ready_rejects_scan_attempt_restarted_before_final_donor_check(self):
        fixture = Fixture(inherited=True)
        reads = 0
        def api(path):
            nonlocal reads
            if path == '/actions/runs/124':
                reads += 1
                if reads > 1:
                    return {**fixture.run, 'run_attempt': 3, 'status': 'in_progress', 'conclusion': None}
            return fixture.api(path)
        plan = {**PLAN, 'sonar_go_paths': '', 'sonar_lcov_paths': '', 'sonar_python_paths': ''}
        result = reuse.find_source(api, fixture.files.__getitem__, plan, IDENTITY, REPO, '1000',
                                   WORKFLOW, PROPERTIES, VARIABLES, set(), INPUTS, True)
        self.assertIsNone(result)
        self.assertEqual(reads, 2)

    def test_ready_rechecks_scan_donor_after_reading_separate_native_donor(self):
        original, current = Fixture(old=True), Fixture(inherited=True)
        scan_restarted = False
        scan_reads = 0
        def api(path):
            nonlocal scan_restarted, scan_reads
            if '/runs?' in path:
                return {'workflow_runs': [current.run, original.run]}
            if path == '/actions/runs/124':
                scan_reads += 1
                if scan_restarted:
                    return {**current.run, 'run_attempt': 3, 'status': 'in_progress', 'conclusion': None}
            if '/actions/runs/123' in path or path.startswith('/git/commits/' + OLD_H) or (
                    path.startswith('/git/commits/' + OLD_M)) or '/git/trees/' + OLD_T in path or (
                    '?ref=' + OLD_H in path or '?ref=' + OLD_M in path):
                scan_restarted = True
                return original.api(path)
            return current.api(path)
        result = reuse.find_source(api, {**original.files, **current.files}.__getitem__, PLAN, IDENTITY,
                                   REPO, '1000', WORKFLOW, PROPERTIES, VARIABLES, set(reuse.DOMAINS), INPUTS, True)
        self.assertIsNone(result)
        self.assertGreaterEqual(scan_reads, 3)

    def test_no_domain_ready_still_requires_real_sonar_clearance(self):
        fixture = Fixture(inherited=True)
        plan = {**PLAN, 'sonar_go_paths': '', 'sonar_lcov_paths': '', 'sonar_python_paths': ''}
        result = reuse.find_source(fixture.api, fixture.files.__getitem__, plan, IDENTITY, REPO, '1000',
                                   WORKFLOW, PROPERTIES, VARIABLES, set(), INPUTS, True)
        self.assertEqual(result['covered'], set())
        self.assertEqual(result['check_id'], 999)


class DonorInventoryTests(unittest.TestCase):
    def test_missing_or_stale_required_job_rejects_donor(self):
        for mutate in (lambda f: f.job.update(name='other'),
                       lambda f: f.job.update(head_sha=OLD_H)):
            with self.subTest(mutation=mutate):
                fixture = Fixture()
                mutate(fixture)
                self.assertIsNone(fixture.find())

    def test_wrong_source_workflow_fails_closed(self):
        fixture = Fixture()
        api = fixture.api
        def wrong(path):
            if path == '/actions/workflows/docker-publish.yml':
                return {'id': 45, 'path': 'other/workflow.yml'}
            return api(path)
        with patch.object(fixture, 'api', wrong), self.assertRaisesRegex(ValueError, 'wrong source workflow'):
            fixture.find()

    def test_untracked_sha_and_push_donor_mismatch_are_skipped(self):
        fixture = Fixture()
        fixture.proof['checkout'] = 'not-a-sha'
        fixture.refresh()
        self.assertIsNone(fixture.find())
        push = Fixture()
        push.run.update(event='push', head_branch='master')
        push.run.pop('pull_requests')
        self.assertIsNone(push.find())

    def test_clearance_requires_unique_check_task_and_stable_completion(self):
        fixture = Fixture()
        fixture.api = lambda path: ({'total_count': 2, 'check_runs': [check()]} if '/check-runs?' in path
                                    else Fixture.api(fixture, path))
        self.assertIsNone(fixture.find(ready=True))
        for mutate in (lambda f: f.job['steps'].remove(
                next(s for s in f.job['steps'] if s['name'] == 'Submit analysis to the existing Sonar project')),
                       lambda f: f.clearance.update(ce_task_id='has spaces!'),
                       lambda f: f.clearance.update(check_completed_at=START)):
            with self.subTest(mutation=mutate):
                fixture = Fixture()
                mutate(fixture)
                fixture.refresh()
                self.assertIsNone(fixture.find(ready=True))

    def test_retained_report_upload_failure_rejects_donor(self):
        fixture = Fixture()
        fixture.job['steps'].remove(
            next(s for s in fixture.job['steps'] if s['name'] == reuse.RETAIN['backend']))
        self.assertIsNone(fixture.find())

    def test_ready_rejects_scan_donor_settling_on_success(self):
        fixture = Fixture()
        fixture.run['conclusion'] = fixture.job['conclusion'] = 'failure'
        fixture.job['steps'].append(dict(name='Require selected packaging validation',
                                         status='completed', conclusion='failure'))
        self.assertEqual(fixture.find(ready=True)['check_id'], 999)
        reads = 0
        api = fixture.api
        def settled(path):
            nonlocal reads
            if path == '/actions/runs/124':
                reads += 1
                if reads >= 4:
                    return {**fixture.run, 'conclusion': 'success'}
            return api(path)
        with patch.object(fixture, 'api', settled):
            self.assertIsNone(fixture.find(ready=True))
    def test_current_run_listing_is_never_its_own_donor(self):
        fixture = Fixture()
        api = fixture.api
        def with_current(path):
            if '/runs?' in path:
                return {'workflow_runs': [dict(id=1000), fixture.run]}
            return api(path)
        with patch.object(fixture, 'api', with_current):
            self.assertEqual(fixture.find()['run'], 124)

    def test_donor_attempt_restart_after_artifact_reads_rejects_result(self):
        fixture = Fixture()
        reads = 0
        api = fixture.api
        def stale(path):
            nonlocal reads
            if path == '/actions/runs/124':
                reads += 1
                if reads >= 3:
                    return {**fixture.run, 'run_attempt': 3}
            return api(path)
        with patch.object(fixture, 'api', stale):
            self.assertIsNone(fixture.find())

    def test_ready_rejects_native_check_changed_after_clearance_reads(self):
        fixture = Fixture()
        calls = 0
        api = fixture.api
        def changed(path):
            nonlocal calls
            if '/check-runs?' in path:
                calls += 1
                if calls == 2:
                    return {'total_count': 1,
                            'check_runs': [{**check(), 'completed_at': '2026-10-12T01:30:00Z'}]}
            return api(path)
        with patch.object(fixture, 'api', changed):
            self.assertIsNone(fixture.find(ready=True))




class RoutingTests(unittest.TestCase):
    def test_non_pr_reuse_preserves_legacy_without_pr_plan_checkout_or_api(self):
        for event in ('push', 'workflow_dispatch'):
            with self.subTest(event=event), tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / 'outputs'
                with patch.dict(reuse.os.environ, {'GITHUB_EVENT_NAME': event, 'GITHUB_OUTPUT': str(target)}, clear=True), \
                        patch.object(reuse.sys, 'argv', ['reuse_sonar_validation.py']), \
                        patch.object(reuse, 'reuse_master') as legacy, \
                        patch.object(reuse, 'current_context', side_effect=AssertionError('PR context accessed')):
                    reuse.main()
                legacy.assert_called_once_with()
                output = target.read_text()
                self.assertIn('sonar_clearance_reused=false\n', output)
                self.assertIn('pr_reuse_validated=false\n', output)
                self.assertIn('sonar_check_id=\n', output)


class RecordTests(unittest.TestCase):
    def test_records_only_executed_native_domains(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'validation-evidence.json'
            env = dict(GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                       EXECUTED_BACKEND='true', EXECUTED_FRONTEND='false', EXECUTED_PYTHON='true',
                       SONAR_VALIDATION_EVIDENCE_PATH=str(target))
            with patch.dict(reuse.os.environ, env):
                reuse.record_validation(PLAN, WORKFLOW, VARIABLES, INPUTS)
            proof = json.loads(target.read_bytes())
            self.assertEqual(proof['domains'], dict(backend=True, frontend=False, python=True))
            self.assertEqual(proof['tree'], T)
            self.assertIs(proof['build_image'], False)

    def test_record_image_provenance_requires_boolean_and_preserves_selection(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                       EXECUTED_BACKEND='true', EXECUTED_FRONTEND='true', EXECUTED_PYTHON='false',
                       SONAR_VALIDATION_EVIDENCE_PATH=str(Path(directory) / 'validation-evidence.json'))
            with patch.dict(reuse.os.environ, env):
                reuse.record_validation({**PLAN, 'build_image': True}, WORKFLOW, VARIABLES, INPUTS)
                proof = json.loads(Path(env['SONAR_VALIDATION_EVIDENCE_PATH']).read_bytes())
                self.assertIs(proof['build_image'], True)
                for value in ('true', 1, None):
                    with self.assertRaisesRegex(ValueError, 'image-build selection'):
                        reuse.record_validation({**PLAN, 'build_image': value}, WORKFLOW, VARIABLES, INPUTS)

    def clearance_environment(self, directory):
        return dict(GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                    SONAR_SCAN_SUCCEEDED='true', SONAR_SCAN_STARTED_AT=SCAN,
                    SONAR_CLEARANCE_PATH=str(Path(directory) / 'sonar-clearance.json'),
                    GITHUB_OUTPUT=str(Path(directory) / 'outputs'))

    def test_clearance_records_actual_task_native_check_and_current_full_tree(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.clearance_environment(directory)
            with patch.dict(reuse.os.environ, env), patch.object(reuse, 'current_pr', return_value=IDENTITY) as verify, \
                    patch.object(reuse.subprocess, 'check_output', side_effect=[H + '\n', T + '\n']), \
                    patch.object(Path, 'read_text', return_value='serverUrl=https://sonarcloud.io\nceTaskId=real-task\n'), \
                    patch.object(reuse, 'find_check', return_value=check()):
                reuse.record_clearance(PLAN, WORKFLOW, PROPERTIES, api=lambda p: None, sleeper=lambda n: None)
            proof = json.loads(Path(env['SONAR_CLEARANCE_PATH']).read_bytes())
            self.assertEqual(proof['analyzed_revision'], H)
            self.assertEqual(proof['tree'], T)
            self.assertEqual(proof['base'], B)
            self.assertEqual(proof['ce_task_id'], 'real-task')
            self.assertEqual(proof['sonar_check_id'], 999)
            self.assertEqual(proof['quality_gate'], 'OK')
            self.assertEqual(verify.call_count, 2)

    def test_clearance_rejects_base_change_during_native_check_polling(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.clearance_environment(directory)
            with patch.dict(reuse.os.environ, env), \
                    patch.object(reuse, 'current_pr', side_effect=[IDENTITY, ValueError('base changed')]), \
                    patch.object(reuse.subprocess, 'check_output', side_effect=[H + '\n', T + '\n']), \
                    patch.object(Path, 'read_text', return_value='serverUrl=https://sonarcloud.io\nceTaskId=real-task\n'), \
                    patch.object(reuse, 'find_check', return_value=check()):
                with self.assertRaisesRegex(ValueError, 'base changed'):
                    reuse.record_clearance(PLAN, WORKFLOW, PROPERTIES, api=lambda p: None, sleeper=lambda n: None)
            self.assertFalse(Path(env['SONAR_CLEARANCE_PATH']).exists())

    def test_clearance_rejects_wrong_scanner_checkout_and_unconfirmed_native_check(self):
        for scanner_head, native_success in [(OLD_H, True), (H, False)]:
            with tempfile.TemporaryDirectory() as directory:
                env = self.clearance_environment(directory)
                with patch.dict(reuse.os.environ, env), \
                        patch.object(reuse, 'current_pr', return_value=IDENTITY), \
                        patch.object(reuse.subprocess, 'check_output', side_effect=[scanner_head + '\n', T + '\n']), \
                        patch.object(Path, 'read_text', return_value='serverUrl=https://sonarcloud.io\nceTaskId=real-task\n'), \
                        patch.object(reuse, 'find_check', side_effect=None if native_success else ValueError('old check')):
                    with self.assertRaises(ValueError):
                        reuse.record_clearance(PLAN, WORKFLOW, PROPERTIES, api=lambda p: None, sleeper=lambda n: None)
                self.assertFalse(Path(env['SONAR_CLEARANCE_PATH']).exists())

    def test_absolute_identity_api_paths_are_adapted_to_repo_relative_reads(self):
        fixture = Fixture()
        def api(path):
            if path == '/pulls/65':
                return dict(state='open', mergeable=True, merge_commit_sha=M,
                            head={'sha': H, 'repo': {'full_name': REPO}},
                            base={'sha': B, 'ref': 'master', 'repo': {'full_name': REPO}})
            return fixture.api(path)
        self.assertEqual(reuse.current_pr(api, PLAN, REPO), IDENTITY)

    def test_clearance_requires_scan_success_not_submission_claim(self):
        with patch.dict(reuse.os.environ, {'SONAR_SCAN_SUCCEEDED': 'false'}):
            with self.assertRaises(ValueError):
                reuse.record_clearance(PLAN, WORKFLOW, PROPERTIES)


class InputBindingTests(unittest.TestCase):
    def test_fingerprint_rejects_unsupported_and_duplicate_entries(self):
        with self.assertRaisesRegex(ValueError, 'unsupported source inventory'):
            reuse.fingerprint([dict(path='tree-dir', mode='040000', type='tree', sha='5' * 40)], VARIABLES)
        with self.assertRaisesRegex(ValueError, 'unsupported source inventory'):
            reuse.fingerprint([dict(path='app.go', mode='100644', type='blob', sha='not-a-sha')], VARIABLES)
        with self.assertRaisesRegex(ValueError, 'duplicate tracked input'):
            reuse.fingerprint(ENTRIES + [dict(ENTRIES[0])], VARIABLES)

    def test_local_inputs_parses_nul_terminated_tree_rows(self):
        raw = b''.join(f"{entry['mode']} {entry['type']} {entry['sha']}\t{entry['path']}".encode() + b'\0'
                       for entry in ENTRIES)
        with patch.object(reuse.subprocess, 'check_output', return_value=raw) as git:
            self.assertEqual(reuse.local_inputs(M, VARIABLES), INPUTS)
        self.assertEqual(git.call_args.args[0], ['git', 'ls-tree', '-r', '-z', M])

    def test_remote_inputs_bind_commit_tree_and_donor_content(self):
        tree = {'sha': T, 'truncated': False,
                'tree': ENTRIES + [dict(path='sub', mode='040000', type='tree', sha='9' * 40)]}
        commit = {'sha': M, 'tree': {'sha': T}}
        def api(path):
            if path == '/git/commits/' + M:
                return commit
            if path == '/git/trees/' + T + '?recursive=1':
                return tree
            if path == '/contents/' + reuse.WORKFLOW + '?ref=' + M:
                return {'encoding': 'base64', 'content': base64.b64encode(WORKFLOW).decode()}
            return {'encoding': 'text'}
        self.assertEqual(reuse.remote_inputs(api, M, VARIABLES), (INPUTS, T))
        self.assertEqual(reuse.content(api, reuse.WORKFLOW, M), WORKFLOW)
        for change in ({'sha': '0' * 40}, {'tree': {'sha': 'short'}}):
            def bad(path, changed={**commit, **change}):
                return changed if '/commits/' in path else api(path)
            with self.assertRaisesRegex(ValueError, 'invalid donor commit'):
                reuse.remote_inputs(bad, M, VARIABLES)
        with self.assertRaisesRegex(ValueError, 'missing donor file'):
            reuse.content(api, 'other-file', M)

    def test_download_rejects_nonpositive_or_noninteger_artifact_identity(self):
        for ident in ('123', 0, -7, True, 1.5):
            with self.subTest(identity=ident), self.assertRaisesRegex(ValueError, 'invalid artifact identity'):
                reuse.download(ident)

    def test_download_uses_bounded_token_free_read(self):
        opener = MagicMock()
        response = opener.open.return_value.__enter__.return_value
        response.read.return_value = b'zipped-report'
        env = dict(GITHUB_REPOSITORY=REPO, GH_TOKEN='secret-token')
        with patch.dict(reuse.os.environ, env, clear=True), \
                patch.object(reuse.urllib.request, 'build_opener', return_value=opener) as build:
            self.assertEqual(reuse.download(12345), b'zipped-report')
        build.assert_called_once()
        self.assertIsInstance(build.call_args.args[0], reuse.TokenFreeRedirect)
        req = opener.open.call_args.args[0]
        self.assertEqual(req.full_url,
                         'https://api.github.com/repos/rzp-labs/vexxx-stash/actions/artifacts/12345/zip')
        self.assertEqual(req.get_header('Authorization'), 'Bearer secret-token')
        response.read.assert_called_once_with(reuse.LIMIT + 1)

    def test_link_and_step_helpers_reject_unparsed_and_partial_failures(self):
        for wrong in (123, 'https://sonarcloud.io/dashboard?bad'):
            self.assertFalse(reuse.sonar_check_link(wrong, 65))
        fixture = Fixture()
        fixture.job['conclusion'] = 'failure'
        self.assertFalse(reuse.eligible_scan_donor(fixture.run, fixture.job))
        for value in ('yes', 1, None):
            self.assertFalse(reuse.domain_covered(fixture.job, 'backend', value))

    def test_executed_domain_selection_rejects_unparsed_values(self):
        env = dict(GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                   EXECUTED_BACKEND='yes', EXECUTED_FRONTEND='false', EXECUTED_PYTHON='true',
                   SONAR_VALIDATION_EVIDENCE_PATH='/nonexistent/evidence.json')
        with patch.dict(reuse.os.environ, env), self.assertRaisesRegex(ValueError, 'executed-domain'):
            reuse.record_validation(PLAN, WORKFLOW, VARIABLES, INPUTS)

    def test_clearance_requires_sonarcloud_task_url_and_valid_id(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dict(GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                       SONAR_SCAN_SUCCEEDED='true', SONAR_SCAN_STARTED_AT=SCAN,
                       SONAR_CLEARANCE_PATH=str(Path(directory) / 'sonar-clearance.json'),
                       GITHUB_OUTPUT=str(Path(directory) / 'outputs'))
            reports = ('serverUrl=https://example.test\nceTaskId=real-task\n',
                       'serverUrl=https://sonarcloud.io\nceTaskId=has spaces!\n')
            for report in reports:
                with self.subTest(report=report), \
                        patch.dict(reuse.os.environ, env), \
                        patch.object(reuse, 'current_pr', return_value=IDENTITY), \
                        patch.object(reuse.subprocess, 'check_output', side_effect=[H + '\n', T + '\n']), \
                        patch.object(Path, 'read_text', return_value=report), \
                        patch.object(reuse, 'find_check', return_value=check()):
                    with self.assertRaisesRegex(ValueError, 'missing expected Sonar analysis task'):
                        reuse.record_clearance(PLAN, WORKFLOW, PROPERTIES,
                                               api=lambda p: None, sleeper=lambda n: None)
            self.assertFalse(Path(env['SONAR_CLEARANCE_PATH']).exists())


class MainFlowTests(unittest.TestCase):
    def call_main(self, env, argv, source=None):
        with patch.dict(reuse.os.environ, env, clear=True), \
                patch.object(reuse.sys, 'argv', argv), \
                patch.object(reuse, 'current_context', return_value=(WORKFLOW, PROPERTIES, VARIABLES)), \
                patch.object(reuse, 'local_inputs', return_value=INPUTS), \
                patch.object(reuse, 'current_pr', return_value=IDENTITY), \
                patch.object(reuse, 'find_source', return_value=source, side_effect=None) as find:
            if isinstance(source, Exception):
                find.side_effect = source
            reuse.main()
            return find

    def base_env(self, directory, action='synchronize', event_name='pull_request', required='false'):
        root = Path(directory)
        event_path = root / 'event.json'
        event_path.write_text(json.dumps({'number': 65, 'action': action}))
        plan_path = root / 'plan.json'
        plan_path.write_text(json.dumps({**PLAN, 'action': action}))
        return dict(GITHUB_EVENT_NAME=event_name, VALIDATION_PLAN_PATH=str(plan_path),
                    GITHUB_EVENT_PATH=str(event_path), GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='1000',
                    REQUIRED_BACKEND=required, REQUIRED_FRONTEND=required, REQUIRED_PYTHON=required,
                    GITHUB_OUTPUT=str(root / 'outputs'), GITHUB_STEP_SUMMARY=str(root / 'summary'))

    def test_current_context_binds_checkout_files_and_variables(self):
        variables = {**VARIABLES, 'POSTHOG_CLI_HOST': 'https://example.test'}
        with patch.object(reuse.subprocess, 'check_output', side_effect=[M + '\n', T + '\n']), \
                patch.dict(reuse.os.environ, {'POSTHOG_CLI_HOST': 'https://example.test'}, clear=True):
            workflow, properties, bound = reuse.current_context(PLAN)
        self.assertEqual(workflow, Path(reuse.WORKFLOW).read_bytes())
        self.assertEqual(properties, Path('sonar-project.properties').read_bytes())
        self.assertEqual(bound, variables)
        with patch.object(reuse.subprocess, 'check_output', side_effect=[M + '\n', OLD_T + '\n']), \
                patch.dict(reuse.os.environ, {}, clear=True), \
                self.assertRaisesRegex(ValueError, 'differs from plan'):
            reuse.current_context(PLAN)

    def test_record_modes_write_proof_and_clearance_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(VALIDATION_PLAN_PATH=str(root / 'plan.json'),
                       SONAR_VALIDATION_EVIDENCE_PATH=str(root / 'evidence.json'),
                       GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                       EXECUTED_BACKEND='true', EXECUTED_FRONTEND='false', EXECUTED_PYTHON='false')
            (root / 'plan.json').write_text(json.dumps(PLAN))
            self.call_main(env, ['reuse_sonar_validation.py', 'record-validation'])
            proof = json.loads((root / 'evidence.json').read_bytes())
            self.assertEqual(proof['schema'], 1)
            self.assertEqual(proof['domains'], dict(backend=True, frontend=False, python=False))
            env = {**env, 'SONAR_CLEARANCE_PATH': str(root / 'clearance.json'),
                   'GITHUB_OUTPUT': str(root / 'outputs'), 'SONAR_SCAN_SUCCEEDED': 'true',
                   'SONAR_SCAN_STARTED_AT': SCAN}
            with patch.object(reuse, 'record_clearance') as record:
                self.call_main(env, ['reuse_sonar_validation.py', 'record-clearance'])
            record.assert_called_once_with(PLAN, WORKFLOW, PROPERTIES)

    def test_invalid_mode_and_event_mismatch_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory)
            with self.assertRaisesRegex(ValueError, 'invalid helper event/mode'):
                self.call_main(env, ['reuse_sonar_validation.py', 'bogus'])
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory)
            Path(env['GITHUB_EVENT_PATH']).write_text(json.dumps({'number': 66, 'action': 'synchronize'}))
            with self.assertRaisesRegex(ValueError, 'event identity mismatch'):
                self.call_main(env, ['reuse_sonar_validation.py'])

    def test_invalid_required_selection_and_ready_without_clearance_fail(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory, required='maybe')
            with self.assertRaisesRegex(ValueError, 'required-domain'):
                self.call_main(env, ['reuse_sonar_validation.py'])
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory, action='ready_for_review')
            with self.assertRaisesRegex(ValueError, 'readiness requires'):
                self.call_main(env, ['reuse_sonar_validation.py'], source=None)

    def test_reuse_writes_reports_outputs_and_summary(self):
        reports = {'dist/coverage/go.out': b'mode: atomic\nfile.go:1.1,1.3 1 1\n'}
        source = dict(covered={'backend'}, reports=reports, run=124, attempt=2, revision=M, tree=T,
                      check_id='')
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory, required='true')
            cwd = os.getcwd()
            try:
                os.chdir(directory)
                find = self.call_main(env, ['reuse_sonar_validation.py'], source=source)
            finally:
                os.chdir(cwd)
            self.assertEqual(find.call_count, 1)
            output = Path(env['GITHUB_OUTPUT']).read_text()
            self.assertIn('pr_reuse_validated=true\n', output)
            self.assertIn('reuse_run=124\n', output)
            self.assertIn('execute_tests=true\n', output)
            self.assertIn('reused_backend=true\n', output)
            self.assertIn('execute_frontend=true\n', output)
            self.assertIn('reuse_workflow_digest=' + reuse.digest(WORKFLOW) + '\n', output)
            self.assertEqual((Path(directory) / 'dist/coverage/go.out').read_bytes(), reports['dist/coverage/go.out'])
            text = Path(env['GITHUB_STEP_SUMMARY']).read_text()
            self.assertIn('### Pre-review validation', text)
            self.assertIn('Native checks reused: backend', text)
            self.assertIn('execute: frontend, python', text)

    def test_find_source_error_falls_back_and_writes_none_reused(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory, required='true')
            self.call_main(env, ['reuse_sonar_validation.py'], source=ValueError('api down'))
            output = Path(env['GITHUB_OUTPUT']).read_text()
            self.assertIn('pr_reuse_validated=false\n', output)
            self.assertIn('execute_tests=true\n', output)
            text = Path(env['GITHUB_STEP_SUMMARY']).read_text()
            self.assertIn('Native checks reused: none', text)

    def test_ready_mode_verifies_identity_again_and_records_clearance(self):
        source = dict(covered=set(), reports={}, run=124, attempt=2, revision=M, tree=T, check_id=999)
        with tempfile.TemporaryDirectory() as directory:
            env = self.base_env(directory, action='ready_for_review')
            with patch.object(reuse, 'current_context', return_value=(WORKFLOW, PROPERTIES, VARIABLES)), \
                    patch.object(reuse, 'local_inputs', return_value=INPUTS), \
                    patch.object(reuse, 'current_pr', return_value=IDENTITY) as verify, \
                    patch.object(reuse, 'find_source', return_value=source), \
                    patch.dict(reuse.os.environ, env, clear=True), \
                    patch.object(reuse.sys, 'argv', ['reuse_sonar_validation.py']):
                reuse.main()
            self.assertEqual(verify.call_count, 2)
            output = Path(env['GITHUB_OUTPUT']).read_text()
            self.assertIn('sonar_clearance_reused=true\n', output)
            self.assertIn('sonar_check_id=999\n', output)
            text = Path(env['GITHUB_STEP_SUMMARY']).read_text()
            self.assertIn('Exact current-tree Sonar clearance verified before readiness.', text)

    def test_module_entrypoint_executes_record_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'plan.json').write_text(json.dumps(PLAN))
            env = dict(VALIDATION_PLAN_PATH=str(root / 'plan.json'),
                       SONAR_VALIDATION_EVIDENCE_PATH=str(root / 'evidence.json'),
                       GITHUB_REPOSITORY=REPO, GITHUB_RUN_ID='124', GITHUB_RUN_ATTEMPT='2',
                       EXECUTED_BACKEND='true', EXECUTED_FRONTEND='false', EXECUTED_PYTHON='false')
            ls_tree = b''.join(f"{entry['mode']} {entry['type']} {entry['sha']}\t{entry['path']}".encode()
                               + b'\0' for entry in ENTRIES)
            def git(command, **kwargs):
                if command[1] == 'rev-parse':
                    return (M if command[2] == 'HEAD' else T) + '\n'
                if command[1] == 'ls-tree':
                    return ls_tree
                raise AssertionError(command)
            original = Path.read_bytes
            def read_bytes(self):
                if str(self) == reuse.WORKFLOW:
                    return WORKFLOW
                return original(self)
            with patch.dict(os.environ, env, clear=True), \
                    patch.object(reuse.sys, 'argv', ['reuse_sonar_validation.py', 'record-validation']), \
                    patch.object(reuse.subprocess, 'check_output', side_effect=git), \
                    patch.object(Path, 'read_bytes', read_bytes):
                runpy.run_path(str(Path(reuse.__file__)), run_name='__main__')
            proof = json.loads((root / 'evidence.json').read_bytes())
            self.assertEqual(proof['checkout'], M)
            self.assertEqual(proof['run'], 124)


if __name__ == '__main__':
    unittest.main()
