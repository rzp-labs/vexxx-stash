import base64
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import reuse_master_validation as reuse

SHA = 'a' * 40
REPO = 'rzp-labs/vexxx-stash'
WORKFLOW_BYTES = b'immutable workflow revision'


def evidence():
    run = dict(id=123, run_attempt=2, event='push', head_branch='master', head_sha=SHA,
               workflow_id=45, path=reuse.WORKFLOW, repository={'full_name': REPO},
               head_repository={'full_name': REPO}, status='completed', conclusion='success')
    jobs = [dict(name=name, head_sha=SHA, run_id=123, run_attempt=2,
                 status='completed', conclusion='success', steps=[])
            for name in ('ci-required',)]
    jobs[0]['steps'] = [dict(name=name, status='completed', conclusion='success')
                        for names in reuse.DOMAINS.values() for name in names]
    jobs[0]['steps'] += [dict(name=name, status='completed', conclusion='success') for name in (
        'Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
        'Verify exact-master validation evidence', 'Verify every selected obligation')]
    return run, jobs


class ReuseTests(unittest.TestCase):
    def api(self, run=None, jobs=None):
        default_run, default_jobs = evidence()
        run, jobs = run or default_run, default_jobs if jobs is None else jobs
        responses = {
            f"/actions/runs/{run['id']}": run,
            '/actions/workflows/docker-publish.yml': {'id': 45, 'path': reuse.WORKFLOW},
            '/contents/' + reuse.WORKFLOW + '?ref=' + SHA: {
                'encoding': 'base64', 'content': base64.b64encode(WORKFLOW_BYTES).decode()},
            f"/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100": {
                'jobs': jobs, 'total_count': len(jobs)},
        }

        def api(path):
            if '/runs?' in path:
                return {'workflow_runs': [run]}
            return responses[path]
        return api

    def find(self, api, required=None, **overrides):
        options = dict(repository=REPO, sha=SHA, current_run='999', workflow_sha=SHA,
                       workflow_bytes=WORKFLOW_BYTES)
        return reuse.find_source(api, set(reuse.DOMAINS) if required is None else required,
                                 **{**options, **overrides})

    def test_exact_success_and_current_attempt_complete_coverage(self):
        source, covered = self.find(self.api())
        self.assertEqual(covered, set(reuse.DOMAINS))
        self.assertEqual((source['id'], source['run_attempt']), (123, 2))

    def test_wrong_sha_event_branch_repo_workflow_status_and_attempt_fail_closed(self):
        run, jobs = evidence()
        for field, values in {
            'head_sha': ['b' * 40], 'event': ['pull_request', 'workflow_dispatch'],
            'head_branch': ['feature'], 'path': ['.github/workflows/other.yml'],
            'workflow_id': [46], 'repository': [{'full_name': 'attacker/fork'}],
            'head_repository': [{'full_name': 'attacker/fork'}],
            'status': ['queued', 'in_progress'], 'conclusion': ['failure', 'cancelled', 'skipped', None],
            'run_attempt': [1, 0, '2'], 'id': [124, 0, '123'],
        }.items():
            for value in values:
                with self.subTest(field=field, value=value):
                    changed = {**run, field: value}
                    self.assertEqual(reuse.coverage(changed, jobs, REPO, SHA, 45), set())

    def test_missing_failed_skipped_or_stale_required_jobs_rejects_all_domains(self):
        run, jobs = evidence()
        for index in range(len(jobs)):
            self.assertEqual(reuse.coverage(run, jobs[:index] + jobs[index + 1:], REPO, SHA, 45), set())
            self.assertEqual(reuse.coverage(run, jobs + [jobs[index]], REPO, SHA, 45), set())
            for field, value in [('conclusion', 'skipped'), ('conclusion', 'failure'),
                                 ('conclusion', 'cancelled'), ('status', 'in_progress'),
                                 ('head_sha', 'b' * 40), ('run_attempt', 1), ('run_id', 456)]:
                changed = copy.deepcopy(jobs)
                changed[index][field] = value
                self.assertEqual(reuse.coverage(run, changed, REPO, SHA, 45), set())

    def test_classifier_and_final_gate_must_have_executed_successfully(self):
        run, jobs = evidence()
        for name in ('Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
                     'Verify exact-master validation evidence', 'Verify every selected obligation'):
            for outcome in ('skipped', 'failure', 'cancelled'):
                changed = copy.deepcopy(jobs)
                next(step for step in changed[0]['steps'] if step['name'] == name)['conclusion'] = outcome
                self.assertEqual(reuse.coverage(run, changed, REPO, SHA, 45), set())

    def test_domain_coverage_is_proven_by_executed_steps_not_green_job(self):
        run, jobs = evidence()
        for domain, names in reuse.DOMAINS.items():
            for name in names:
                for outcome in ('skipped', 'failure', 'cancelled', None):
                    changed = copy.deepcopy(jobs)
                    next(step for step in changed[0]['steps'] if step['name'] == name)['conclusion'] = outcome
                    self.assertEqual(reuse.coverage(run, changed, REPO, SHA, 45), set(reuse.DOMAINS) - {domain})
                changed = copy.deepcopy(jobs)
                changed[0]['steps'] = [step for step in changed[0]['steps'] if step['name'] != name]
                self.assertEqual(reuse.coverage(run, changed, REPO, SHA, 45), set(reuse.DOMAINS) - {domain})
                changed = copy.deepcopy(jobs)
                changed[0]['steps'].append(next(step for step in changed[0]['steps'] if step['name'] == name))
                self.assertEqual(reuse.coverage(run, changed, REPO, SHA, 45), set(reuse.DOMAINS) - {domain})

    def test_workflow_revision_and_checked_out_bytes_must_match(self):
        self.assertEqual(self.find(self.api(), workflow_sha='b' * 40), (None, set()))
        self.assertEqual(self.find(self.api(), workflow_bytes=b'changed workflow'), (None, set()))
        self.assertEqual(self.find(self.api(), current_run='123'), (None, set()))

    def test_partial_coverage_runs_only_unproven_domains(self):
        run, jobs = evidence()
        next(step for step in jobs[0]['steps'] if step['name'] == 'Python regression tests')['conclusion'] = 'skipped'
        source, covered = self.find(self.api(run, jobs))
        self.assertEqual(covered, {'backend', 'frontend'})
        self.assertEqual(source['id'], 123)
        self.assertEqual(self.find(self.api(run, jobs), required={'python'}), (None, set()))

    def test_truncated_job_inventory_fails_closed(self):
        api = self.api()

        def truncated(path):
            value = api(path)
            if 'jobs' in value:
                value['total_count'] += 1
            return value
        self.assertEqual(self.find(truncated), (None, set()))

    def test_rerun_started_or_failed_during_lookup_does_not_inherit_success(self):
        api = self.api()
        for change in ({'run_attempt': 3}, {'status': 'in_progress'}, {'conclusion': 'failure'}):
            def fresh(path):
                result = api(path)
                return {**result, **change} if path == '/actions/runs/123' else result
            self.assertEqual(self.find(fresh), (None, set()))

    def test_cli_api_denial_malformed_data_and_positive_proof_outputs(self):
        for failure in (OSError('denied'), ValueError('malformed'), KeyError('missing'), None):
            with tempfile.TemporaryDirectory() as directory:
                env = {**os.environ, 'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_REF_TYPE': 'branch',
                       'GITHUB_REPOSITORY': REPO, 'GITHUB_SHA': SHA, 'GITHUB_RUN_ID': '999',
                       'GITHUB_WORKFLOW_SHA': SHA, 'GH_TOKEN': 'never-print-this',
                       'GITHUB_OUTPUT': str(Path(directory) / 'out'),
                       'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary'),
                       **{'REQUIRED_' + domain.upper(): 'true' for domain in reuse.DOMAINS}}
                with patch.dict(os.environ, env), patch.object(reuse, 'find_source',
                        side_effect=failure, return_value=(evidence()[0], {'backend', 'frontend'})):
                    reuse.main()
                out = dict(line.split('=', 1) for line in Path(env['GITHUB_OUTPUT']).read_text().splitlines())
                self.assertEqual(out['execute_python'], 'true')
                self.assertEqual(out['execute_backend'], 'true' if failure else 'false')
                self.assertEqual(out['reused_frontend'], 'false' if failure else 'true')
                self.assertEqual(out['reuse_run'], '' if failure else '123')
                if not failure:
                    self.assertEqual(out['reuse_revision'], SHA)
                    self.assertIn('/runs/123/attempts/2', Path(env['GITHUB_STEP_SUMMARY']).read_text())
                self.assertNotIn('never-print-this', Path(env['GITHUB_STEP_SUMMARY']).read_text())

    def test_pr_master_and_draft_never_consult_network_or_reuse(self):
        for event, required in [('pull_request', True), ('push', True), ('pull_request', False)]:
            with tempfile.TemporaryDirectory() as directory:
                env = {**os.environ, 'GITHUB_EVENT_NAME': event, 'GITHUB_REF_TYPE': 'branch',
                       'GITHUB_OUTPUT': str(Path(directory) / 'out'),
                       'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary'),
                       **{'REQUIRED_' + domain.upper(): str(required).lower() for domain in reuse.DOMAINS}}
                with patch.dict(os.environ, env), patch.object(reuse, 'find_source') as find:
                    reuse.main()
                find.assert_not_called()
                self.assertIn('execute_tests=' + str(required).lower(), Path(env['GITHUB_OUTPUT']).read_text())
                self.assertIn('reused_backend=false', Path(env['GITHUB_OUTPUT']).read_text())


class RequiredContextTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        workflow = Path(__file__).resolve().parent.parent / reuse.WORKFLOW
        cls.jobs = json.loads(subprocess.check_output([
            'ruby', '-ryaml', '-rjson', '-e', 'puts JSON.generate(YAML.load_file(ARGV[0]))', str(workflow)]))['jobs']
        cls.aggregate = next(step['run'] for step in cls.jobs['ci-required']['steps'] if step.get('name') == 'Verify every selected obligation')

    def gate(self, success, **overrides):
        with tempfile.TemporaryDirectory() as directory:
            env = {**os.environ, 'PLAN_RESULT': 'success', 'DEFERRED': 'false',
                   'DRAFT_EVENT': 'false', 'RUN_TESTS': 'false', 'REQUIRED_TESTS': 'false',
                   'TEST_RESULT': 'skipped', 'BUILD_IMAGE': 'false', 'IMAGE_RESULT': 'skipped',
                   'EXPECTED_SHA': SHA, 'REUSE_ALLOWED': 'true', 'REUSE_RUN': '123',
                   'REUSE_ATTEMPT': '2', 'REUSE_REVISION': SHA, 'REUSE_WORKFLOW_DIGEST': 'd' * 64,
                   **{prefix + domain.upper(): 'false' for prefix in ('', 'REQUIRED_', 'REUSED_')
                      for domain in reuse.DOMAINS},
                   'GITHUB_STEP_SUMMARY': str(Path(directory) / 'summary'), **overrides}
            for domain in ('BACKEND', 'FRONTEND', 'PYTHON'):
                selected = env[domain] == 'true'
                env.setdefault(domain + '_RESULT', env.get('TEST_RESULT', 'success') if selected else 'skipped')
                if domain != 'PYTHON':
                    env.setdefault(domain + '_GENERATE_RESULT', 'success' if selected else 'skipped')
                    env.setdefault(domain + '_COMPILE_RESULT', 'success' if selected and env['BUILD_IMAGE'] == 'false' and env[domain + '_RESULT'] == 'success' else 'skipped')
            env.setdefault('PACKAGING_REUSED', 'false')
            env.setdefault('EXECUTE_IMAGE', env['BUILD_IMAGE'])
            result = subprocess.run(['bash'], input=self.aggregate, text=True, env=env, capture_output=True)
            self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)

    def test_draft_to_ready_and_back_never_emits_required_draft_success(self):
        name = self.jobs['ci-required']['name']
        self.assertEqual(name, "${{ github.event_name == 'pull_request' && github.event.pull_request.draft && 'validation-deferred' || 'ci-required' }}")
        self.gate(True, DRAFT_EVENT='true', DEFERRED='true')
        # A ready event cannot inherit draft deferral even at the same head SHA.
        self.gate(False, DEFERRED='true')
        self.gate(False, DRAFT_EVENT='true', DEFERRED='false')
        for result in ('skipped', 'failure', 'cancelled', ''):
            self.gate(False, REQUIRED_BACKEND='true', REQUIRED_TESTS='true',
                      BACKEND='true', RUN_TESTS='true', TEST_RESULT=result)
        self.gate(True, REQUIRED_BACKEND='true', REQUIRED_TESTS='true',
                  BACKEND='true', RUN_TESTS='true', TEST_RESULT='success')
        self.gate(False, DRAFT_EVENT='true', DEFERRED='true',
                  REQUIRED_BACKEND='true', REQUIRED_TESTS='true', BACKEND='true',
                  RUN_TESTS='true', TEST_RESULT='success')

    def test_full_and_partial_reuse_require_complete_coverage_and_fresh_proof(self):
        reused = {'REQUIRED_TESTS': 'true', **{'REQUIRED_' + domain.upper(): 'true' for domain in reuse.DOMAINS},
                  **{'REUSED_' + domain.upper(): 'true' for domain in reuse.DOMAINS}}
        self.gate(True, **reused)
        # Old-sha, incomplete coverage, foreign events and missing proof fail.
        for override in ({'REUSE_REVISION': 'b' * 40}, {'REUSE_RUN': ''}, {'REUSE_ATTEMPT': ''},
                         {'REUSE_WORKFLOW_DIGEST': ''}, {'REUSE_ALLOWED': 'false'},
                         {'REUSED_PYTHON': 'false'}, {'BACKEND_RESULT': 'success'}, {'PLAN_RESULT': 'cancelled'}):
            self.gate(False, **{**reused, **override})
        partial = {**reused, 'REUSED_PYTHON': 'false', 'PYTHON': 'true', 'RUN_TESTS': 'true'}
        self.gate(True, **{**partial, 'TEST_RESULT': 'success'})
        for result in ('failure', 'cancelled', 'skipped'):
            self.gate(False, **{**partial, 'TEST_RESULT': result})
        self.gate(False, **{**partial, 'TEST_RESULT': 'success', 'REUSED_PYTHON': 'true'})

    def test_image_is_still_required_after_all_tests_reused(self):
        proof = {'REQUIRED_TESTS': 'true', 'REQUIRED_BACKEND': 'true', 'REUSED_BACKEND': 'true',
                 'BUILD_IMAGE': 'true'}
        self.gate(True, **{**proof, 'IMAGE_RESULT': 'success'})
        for result in ('skipped', 'cancelled', 'failure'):
            self.gate(False, **{**proof, 'IMAGE_RESULT': result})
        steps = self.jobs['ci-required']['steps']
        smoke = next(step['run'] for step in steps if step.get('name') == 'Smoke test container')
        save = next(step['run'] for step in steps if step.get('name') == 'Save the exact smoke-tested image')
        load = next(step['run'] for step in self.jobs['publish']['steps'] if step.get('name') == 'Load and verify tested source identity')
        self.assertIn('"$(cat image.id)"', smoke)
        self.assertIn('sha256sum image.tar image.id', save)
        self.assertIn('sha256sum --check image.tar.sha256', load)
        self.assertIn('"$(cat image.id)"', load)
        self.assertNotIn('build-push-action', str(self.jobs['publish']))

    def test_selected_generation_and_nonimage_compilation_cannot_be_skipped(self):
        for domain in ('BACKEND', 'FRONTEND'):
            selected = {'REQUIRED_TESTS': 'true', 'RUN_TESTS': 'true', 'TEST_RESULT': 'success',
                        domain: 'true', 'REQUIRED_' + domain: 'true'}
            self.gate(True, **selected)
            for step in ('_GENERATE_RESULT', '_COMPILE_RESULT', '_RESULT'):
                for outcome in ('failure', 'cancelled', 'skipped'):
                    self.gate(False, **{**selected, domain + step: outcome})
            image = {**selected, 'BUILD_IMAGE': 'true', 'IMAGE_RESULT': 'success'}
            self.gate(True, **image)  # Docker supplies compile coverage.
            self.gate(False, **{**image, domain + '_COMPILE_RESULT': 'success'})

    def test_equivalent_pr_packaging_requires_proof_and_cannot_replace_release_image(self):
        proof = dict(BUILD_IMAGE='true', EXECUTE_IMAGE='false', PACKAGING_REUSED='true',
                     PACKAGING_REUSE_ALLOWED='true', PACKAGING_SOURCE_RUN='123', PACKAGING_SOURCE_ATTEMPT='2',
                     PACKAGING_CANDIDATE='c' * 40, PACKAGING_LANDED=SHA, PACKAGING_TREE='e' * 40,
                     EXPECTED_TREE='e' * 40, PACKAGING_WORKFLOW_DIGEST='d' * 64, PACKAGING_INPUT_DIGEST='f' * 64)
        self.gate(True, **proof)
        for change in (dict(PACKAGING_REUSE_ALLOWED='false'), dict(PACKAGING_SOURCE_RUN=''),
                       dict(PACKAGING_SOURCE_ATTEMPT=''), dict(PACKAGING_CANDIDATE=''),
                       dict(PACKAGING_LANDED='b' * 40), dict(PACKAGING_TREE='b' * 40),
                       dict(PACKAGING_WORKFLOW_DIGEST=''), dict(PACKAGING_INPUT_DIGEST=''),
                       dict(EXECUTE_IMAGE='true'), dict(IMAGE_RESULT='success'), dict(BUILD_IMAGE='false'),
                       dict(DRAFT_EVENT='true', DEFERRED='true'), dict(PACKAGING_REUSED='false'),
                       dict(PACKAGING_REUSED='invalid')):
            with self.subTest(change=change):
                self.gate(False, **{**proof, **change})
        # Packaging evidence never authorizes skipped master test obligations.
        selected = dict(REQUIRED_TESTS='true', RUN_TESTS='true', REQUIRED_BACKEND='true', BACKEND='true',
                        BACKEND_RESULT='success', BACKEND_GENERATE_RESULT='success')
        self.gate(True, **{**proof, **selected})
        for result in ('skipped', 'failure', 'cancelled'):
            self.gate(False, **{**proof, **selected, 'BACKEND_RESULT': result})


if __name__ == '__main__':
    unittest.main()
