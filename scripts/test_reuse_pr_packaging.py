import base64
import copy
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import urllib.request
import zipfile
from unittest.mock import patch
from datetime import datetime

import reuse_pr_packaging as reuse

LANDED, BASE, HEAD, CANDIDATE, TREE = [letter * 40 for letter in 'abcde']
REPO = 'rzp-labs/vexxx-stash'
WORKFLOW = b'immutable workflow'
INPUTS = 'f' * 64


def fixture():
    plan = dict(release_version='0.1.0', checkout=LANDED, head=LANDED, base=BASE, tree=TREE,
                backend=True, frontend=True, python=True, publish=False, build_image=True)
    pr = dict(number=42, merged=True, draft=False, merge_commit_sha=LANDED,
              base=dict(ref='master', sha=BASE, repo=dict(full_name=REPO)),
              head=dict(sha=HEAD, repo=dict(full_name=REPO)))
    run = dict(id=123, run_attempt=2, event='pull_request', head_sha=HEAD,
               repository=dict(full_name=REPO, id=77), head_repository=dict(full_name=REPO),
               path=reuse.WORKFLOW, workflow_id=45, status='completed', conclusion='success')
    names = ['Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
             'Verify exact-master validation evidence', 'Verify PR packaging evidence',
             'Build Linux amd64 image locally', 'Smoke test container', 'Verify every selected obligation',
             'Record PR packaging evidence', 'Save PR packaging evidence']
    names += [name for names in reuse.DOMAINS.values() for name in names]
    job = dict(name='ci-required', head_sha=HEAD, run_id=123, run_attempt=2,
               status='completed', conclusion='success',
               check_run_url='https://api.github.com/repos/' + REPO + '/check-runs/789',
               started_at='2026-10-05T18:00:00Z', completed_at='2026-10-05T18:30:00Z',
               steps=[dict(name=name, status='completed', conclusion='success') for name in names])
    proof = dict(schema=1, run=123, attempt=2, pr=42, repository=REPO, checkout=CANDIDATE,
                 parents=[BASE, HEAD], tree=TREE, input_digest=INPUTS, workflow_digest=reuse.digest(WORKFLOW),
                 workflow_sha=CANDIDATE, domains={key: True for key in reuse.DOMAINS},
                 version='0.1.0-dev+sha.' + CANDIDATE[:12], publish=False, build_image=True,
                 build_date='2026-10-05 18:12:58',
                 materials={ref: '1' * 64 for ref in reuse.bases(Path(reuse.DOCKERFILE).read_text())})
    commit = dict(sha=CANDIDATE, commit=dict(tree=dict(sha=TREE), committer=dict(date='2026-10-05T18:12:58Z')),
                  parents=[dict(sha=BASE), dict(sha=HEAD)])
    content = dict(encoding='base64', content=base64.b64encode(WORKFLOW).decode())
    return dict(plan=plan, pr=pr, run=run, jobs=[job], proof=proof, commit=commit, content=content)


class PackagingReuseTests(unittest.TestCase):
    def find(self, data=None, check_material=None, **overrides):
        data = fixture() if data is None else data
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as writer:
            writer.writestr('pr-packaging.json', json.dumps(data['proof']))
        raw = archive.getvalue()
        artifact = dict(id=999, name='pr-packaging-2', expired=False,
                        workflow_run=dict(id=123), digest='sha256:' + reuse.digest(raw))
        artifact.update(data.get('artifact', {}))
        responses = {
            '/actions/workflows/docker-publish.yml': dict(id=45, path=reuse.WORKFLOW),
            '/commits/' + LANDED + '/pulls?per_page=100': [dict(number=42)],
            '/pulls/42': data['pr'],
            '/actions/runs/123/attempts/2/jobs?per_page=100': dict(total_count=len(data['jobs']), jobs=data['jobs']),
            '/actions/runs/123/artifacts?per_page=100': dict(total_count=1, artifacts=[artifact]),
            '/commits/' + CANDIDATE: data['commit'],
            '/contents/' + reuse.WORKFLOW + '?ref=' + CANDIDATE: data['content'],
            '/actions/runs/123': data.get('fresh', data['run']),
        }
        responses.update(data.get('responses', {}))

        def api(path):
            if '/runs?' in path:
                return {'workflow_runs': [data['run']]}
            return responses[path]
        options = dict(api=api, download=lambda _: raw,
                       check_material=check_material or (lambda ref, sha: True),
                       verify_identity=lambda proof, run, job: True,
                       plan=data['plan'], parents=[BASE, HEAD], repo=REPO,
                       workflow_sha=LANDED, workflow_bytes=WORKFLOW, inputs=INPUTS)
        return reuse.find_source(**{**options, **overrides})

    def test_identical_trusted_merge_reuses_only_packaging_evidence(self):
        source = self.find()
        self.assertEqual(source['candidate'], CANDIDATE)
        self.assertEqual(source['landed'], LANDED)
        self.assertEqual((source['run'], source['attempt']), (123, 2))
        self.assertNotEqual(source['candidate'], source['landed'])

    def test_self_asserted_or_unauthenticated_workflow_claim_cannot_authorize_reuse(self):
        self.assertIsNone(self.find(verify_identity=lambda proof, run, job: False))

    def test_landing_workflow_range_and_ordered_parents_must_match(self):
        for override in (dict(workflow_sha=HEAD), dict(parents=[HEAD, BASE]),
                         dict(parents=[BASE]), dict(parents=[BASE, HEAD, CANDIDATE]),
                         dict(workflow_bytes=b'different workflow'), dict(inputs='0' * 64)):
            with self.subTest(override=override):
                self.assertIsNone(self.find(**override))
        for key, value in [('base', HEAD), ('head', HEAD), ('tree', BASE)]:
            data = fixture(); data['plan'][key] = value
            self.assertIsNone(self.find(data))

    def test_untrusted_unmerged_draft_or_different_pr_fails_closed(self):
        for change in (dict(merged=False), dict(draft=True), dict(merge_commit_sha=HEAD),
                       dict(base=dict(ref='other', sha=BASE, repo=dict(full_name=REPO))),
                       dict(head=dict(sha=HEAD, repo=dict(full_name='attacker/fork'))),
                       dict(head=dict(sha=CANDIDATE, repo=dict(full_name=REPO)))):
            data = fixture(); data['pr'].update(change)
            self.assertIsNone(self.find(data))

    def test_wrong_run_identity_pending_failed_and_wrong_workflow(self):
        for key, values in {
            'event': ['push', 'pull_request_target'], 'head_sha': [CANDIDATE],
            'repository': [dict(full_name='other/repo')], 'head_repository': [dict(full_name='attacker/fork')],
            'path': ['other.yml'], 'workflow_id': [46], 'status': ['in_progress', 'queued'],
            'conclusion': ['failure', 'cancelled', 'skipped', None], 'id': [0, '123'], 'run_attempt': [0, '2'],
        }.items():
            for value in values:
                data = fixture(); data['run'][key] = value
                with self.subTest(key=key, value=value):
                    self.assertIsNone(self.find(data))

    def test_all_required_generation_tests_build_smoke_gate_and_evidence_steps_must_pass(self):
        original = fixture()
        for index, step in enumerate(original['jobs'][0]['steps']):
            for conclusion in ('skipped', 'failure', 'cancelled'):
                data = copy.deepcopy(original); data['jobs'][0]['steps'][index]['conclusion'] = conclusion
                with self.subTest(name=step['name'], result=conclusion):
                    self.assertIsNone(self.find(data))
            data = copy.deepcopy(original); data['jobs'][0]['steps'].pop(index)
            self.assertIsNone(self.find(data))
            data = copy.deepcopy(original); data['jobs'][0]['steps'].append(step)
            self.assertIsNone(self.find(data))

    def test_missing_duplicate_truncated_and_stale_job_evidence(self):
        for key, value in [('name', 'validation-deferred'), ('head_sha', CANDIDATE), ('run_id', 456),
                           ('run_attempt', 1), ('status', 'in_progress'), ('conclusion', 'failure')]:
            data = fixture(); data['jobs'][0][key] = value
            self.assertIsNone(self.find(data))
        data = fixture(); data['jobs'] *= 2
        self.assertIsNone(self.find(data))
        data = fixture(); data['responses'] = {'/actions/runs/123/attempts/2/jobs?per_page=100':
                                             dict(total_count=2, jobs=data['jobs'])}
        self.assertIsNone(self.find(data))
        for change in (dict(run_attempt=3), dict(status='in_progress'), dict(conclusion='failure')):
            data = fixture(); data['fresh'] = {**data['run'], **change}
            self.assertIsNone(self.find(data))

    def test_candidate_proof_identity_workflow_inputs_and_domain_coverage(self):
        for key, value in [('schema', 2), ('run', 456), ('attempt', 1), ('pr', 43), ('repository', 'other/repo'),
                           ('parents', [HEAD, BASE]), ('tree', HEAD), ('input_digest', '0' * 64),
                           ('workflow_digest', '0' * 64), ('workflow_sha', HEAD),
                           ('domains', dict(backend=True, frontend=True, python=False)),
                           ('version', 'release'), ('version', 'ci-' + CANDIDATE[:12]),
                           ('version', '0.2.0-dev+sha.' + CANDIDATE[:12]),
                           ('build_date', '2026-10-05 18:12:59'),
                           ('publish', True), ('build_image', False)]:
            data = fixture(); data['proof'][key] = value
            with self.subTest(key=key):
                self.assertIsNone(self.find(data))
        for key, value in [('sha', HEAD), ('parents', [dict(sha=HEAD), dict(sha=BASE)]),
                           ('commit', dict(tree=dict(sha=HEAD)))]:
            data = fixture(); data['commit'][key] = value
            self.assertIsNone(self.find(data))
        data = fixture(); data['content']['content'] = base64.b64encode(b'changed').decode()
        self.assertIsNone(self.find(data))

    def test_expired_missing_wrong_attempt_or_wrong_run_artifact_rejects(self):
        for change in (dict(expired=True), dict(name='pr-packaging-1'), dict(workflow_run=dict(id=456))):
            data = fixture(); data['artifact'] = change
            self.assertIsNone(self.find(data))
        for artifacts, count in (([], 0), ([dict(name='other')], 1), ([], 1)):
            data = fixture(); data['responses'] = {'/actions/runs/123/artifacts?per_page=100':
                                                 dict(artifacts=artifacts, total_count=count)}
            self.assertIsNone(self.find(data))

    def test_archive_integrity_inventory_and_size_are_checked_without_extraction(self):
        for name in ('../pr-packaging.json', 'other.json'):
            stream = io.BytesIO()
            with zipfile.ZipFile(stream, 'w') as archive:
                archive.writestr(name, '{}')
            raw = stream.getvalue()
            with self.assertRaises(ValueError):
                reuse.unpack(raw, 'sha256:' + reuse.digest(raw))
        with self.assertRaises(ValueError):
            reuse.unpack(b'bad', 'sha256:' + '0' * 64)
        with self.assertRaises(ValueError):
            reuse.unpack(b'x' * (reuse.LIMIT + 1), '')
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, 'w', zipfile.ZIP_DEFLATED) as archive:
            archive.writestr('pr-packaging.json', 'x' * (reuse.LIMIT + 1))
        with self.assertRaises(ValueError):
            reuse.unpack(stream.getvalue(), 'sha256:' + reuse.digest(stream.getvalue()))
        data = fixture(); data['artifact'] = dict(digest='sha256:' + '0' * 64)
        with self.assertRaises(ValueError):
            self.find(data)

    def test_changed_missing_or_unresolved_base_material_falls_back(self):
        self.assertIsNone(self.find(check_material=lambda ref, sha: False))
        data = fixture(); data['proof']['materials'] = {}
        self.assertIsNone(self.find(data))
        data = fixture(); data['proof']['materials'].pop(next(iter(data['proof']['materials'])))
        self.assertIsNone(self.find(data))
        data = fixture(); data['proof']['materials'][next(iter(data['proof']['materials']))] = 'malformed'
        self.assertIsNone(self.find(data))

    def test_declared_variables_tree_and_workflow_fingerprinted_without_values_in_proof(self):
        values = {name: name + '-value' for name in reuse.VARIABLES}
        original = reuse.input_digest(TREE, WORKFLOW, values)
        for name in values:
            changed = {**values, name: 'different'}
            self.assertNotEqual(original, reuse.input_digest(TREE, WORKFLOW, changed))
        self.assertNotEqual(original, reuse.input_digest(HEAD, WORKFLOW, values))
        self.assertNotEqual(original, reuse.input_digest(TREE, b'changed', values))
        self.assertEqual(len(original), 64)

    def test_provenance_includes_every_base_and_only_supported_materials(self):
        metadata = {'buildx.build.provenance': {'materials': [
            dict(uri='pkg:docker/library/alpine@3.24?platform=linux%2Famd64', digest=dict(sha256='1' * 64))]}}
        self.assertEqual(reuse.materials(metadata, 'FROM alpine:3.24 AS base\nFROM base'),
                         {'alpine:3.24': '1' * 64})
        for dockerfile in ('FROM node:24-alpine', 'FROM ${BASE}', 'FROM --platform=linux/arm64 alpine:3.24'):
            with self.assertRaises(ValueError):
                reuse.materials(metadata, dockerfile)
        for uri in ('https://other', 'pkg:docker/alpine@3.24?platform=linux%2Farm64'):
            changed = copy.deepcopy(metadata); changed['buildx.build.provenance']['materials'][0]['uri'] = uri
            with self.assertRaises(ValueError):
                reuse.materials(changed, 'FROM alpine:3.24')

    def test_resolved_manifest_platform_and_changed_tag(self):
        raw = json.dumps({'manifests': [dict(digest='sha256:' + '1' * 64,
                                           platform=dict(os='linux', architecture='amd64'))]}).encode()
        with patch.object(reuse.subprocess, 'check_output', return_value=raw) as read:
            self.assertTrue(reuse.current_material('alpine:3.24', '1' * 64))
            self.assertTrue(reuse.current_material('alpine:3.24', reuse.digest(raw)))
            self.assertFalse(reuse.current_material('alpine:3.24', '2' * 64))
        self.assertEqual(read.call_args.args[0], ['docker', 'buildx', 'imagetools', 'inspect', 'alpine:3.24', '--raw'])

    def test_artifact_redirect_drops_authorization_and_rejects_http(self):
        handler = reuse.TokenFreeRedirect()
        request = urllib.request.Request('https://api.github.com/artifact', headers={'Authorization': 'Bearer test'})
        redirected = handler.redirect_request(request, None, 302, '', {}, 'https://storage.example/archive')
        self.assertIsNone(redirected.get_header('Authorization'))
        with self.assertRaises(ValueError):
            handler.redirect_request(request, None, 302, '', {}, 'http://storage.example/archive')

    def main(self, event='push', ref='master', error=None, source=None):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = fixture(); (root / 'plan').write_text(json.dumps(data['plan']))
            env = {**os.environ, 'VALIDATION_PLAN_PATH': str(root / 'plan'), 'GITHUB_SHA': LANDED,
                   'GITHUB_EVENT_NAME': event, 'GITHUB_REF_TYPE': 'branch', 'GITHUB_REF_NAME': ref,
                   'GITHUB_REPOSITORY': REPO, 'GITHUB_WORKFLOW_SHA': LANDED,
                   'GITHUB_OUTPUT': str(root / 'output'), 'GITHUB_STEP_SUMMARY': str(root / 'summary')}
            with patch.dict(os.environ, env), patch.object(reuse, 'checkout_identity', return_value=TREE), \
                    patch.object(reuse.subprocess, 'check_output', return_value=f'{LANDED} {BASE} {HEAD}'), \
                    patch.object(reuse, 'find_source', side_effect=error, return_value=source) as find, \
                    patch.object(reuse.sys, 'argv', ['reuse_pr_packaging.py']):
                reuse.main()
            return (root / 'output').read_text(), (root / 'summary').read_text(), find.call_count

    def test_cli_api_or_registry_denial_malformed_missing_proof_builds_normally(self):
        for error in (None, OSError('unavailable'), ValueError('malformed'), KeyError('missing'),
                      subprocess.TimeoutExpired('registry-read', 30)):
            output, summary, calls = self.main(error=error)
            self.assertIn('reused=false\n', output)
            self.assertIn('execute_image=true\n', output)
            self.assertNotIn('unavailable', summary)
            self.assertEqual(calls, 1)

    def test_cli_never_reuses_for_pr_release_dispatch_or_nonmaster(self):
        for event, ref in [('pull_request', '42/merge'), ('workflow_dispatch', 'master'), ('push', 'feature')]:
            output, _, calls = self.main(event=event, ref=ref)
            self.assertIn('execute_image=true\n', output)
            self.assertIn('reused=false\n', output)
            self.assertEqual(calls, 0)

    def test_cli_reuse_summary_keeps_landed_and_candidate_distinct(self):
        source = dict(run=123, attempt=2, candidate=CANDIDATE, landed=LANDED, pr=42)
        output, summary, _ = self.main(source=source)
        self.assertIn('execute_image=false\n', output)
        self.assertIn('reused=true\n', output)
        self.assertIn(CANDIDATE, summary)
        self.assertIn(LANDED, summary)
        self.assertIn('No landed image was built or promoted', summary)

    def test_evidence_recorder_stores_hashes_and_actual_materials_only(self):
        metadata = {'buildx.build.provenance': {'materials': [
            dict(uri='pkg:docker/' + ref.replace(':', '@') + '?platform=linux%2Famd64',
                 digest=dict(sha256='1' * 64))
            for ref in reuse.bases(Path(reuse.DOCKERFILE).read_text())]}}
        for available in (True, False):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                plan = fixture()['plan']
                plan.update(checkout=CANDIDATE, version='0.1.0-dev+sha.' + CANDIDATE[:12], build_date='2026-10-05 18:12:58')
                (root / 'plan').write_text(json.dumps(plan))
                env = {**os.environ, 'VALIDATION_PLAN_PATH': str(root / 'plan'), 'GITHUB_SHA': CANDIDATE,
                       'PACKAGING_EVIDENCE_PATH': str(root / 'pr-packaging.json'),
                       'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2', 'PR_NUMBER': '42',
                       'GITHUB_REPOSITORY': REPO, 'GITHUB_WORKFLOW_SHA': CANDIDATE,
                       'GITHUB_OUTPUT': str(root / 'output'), 'GITHUB_STEP_SUMMARY': str(root / 'summary'),
                       'BUILD_METADATA': json.dumps(metadata if available else {}),
                       'VITE_PUBLIC_POSTHOG_PROJECT_TOKEN': 'test-never-write-this-value'}
                with patch.dict(os.environ, env), patch.object(reuse, 'checkout_identity', return_value=TREE), \
                        patch.object(reuse.subprocess, 'check_output', return_value=f'{CANDIDATE} {BASE} {HEAD}'), \
                        patch.object(reuse, 'identity_receipt', return_value='test-receipt'), \
                        patch.object(reuse.sys, 'argv', ['reuse_pr_packaging.py', 'record']):
                    reuse.main()
                self.assertIn('recorded=' + str(available).lower(), (root / 'output').read_text())
                self.assertEqual((root / 'pr-packaging.json').exists(), available)
                if available:
                    text = (root / 'pr-packaging.json').read_text(); proof = json.loads(text)
                    self.assertEqual(proof['checkout'], CANDIDATE)
                    self.assertEqual(proof['parents'], [BASE, HEAD])
                    self.assertEqual(set(proof['materials']), reuse.bases(Path(reuse.DOCKERFILE).read_text()))
                    self.assertNotIn('test-never-write-this-value', text)
                self.assertNotIn('test-never-write-this-value', (root / 'summary').read_text())

    def test_fresh_landed_identity_mismatch_cannot_be_hidden_by_reuse(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'plan'; path.write_text(json.dumps(fixture()['plan']))
            with patch.dict(os.environ, VALIDATION_PLAN_PATH=str(path), GITHUB_SHA=LANDED), \
                    patch.object(reuse, 'checkout_identity', return_value=BASE), \
                    patch.object(reuse, 'find_source') as find:
                with self.assertRaises(ValueError):
                    reuse.main()
                find.assert_not_called()


class SignedWorkflowReceiptTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.private = Path(cls.directory.name) / 'test-only-key.pem'
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt', 'rsa_keygen_bits:2048',
                        '-out', str(cls.private)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        modulus = subprocess.check_output(['openssl', 'rsa', '-in', str(cls.private), '-noout', '-modulus'],
                                          stderr=subprocess.DEVNULL).decode().strip().split('=')[1]
        cls.keys = {'keys': [dict(kid='test-key', kty='RSA', alg='RS256', use='sig',
                                 n=cls.encode(bytes.fromhex(modulus)), e=cls.encode(b'\x01\x00\x01'))]}

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    @staticmethod
    def encode(value):
        return base64.urlsafe_b64encode(value).decode().rstrip('=')

    def receipt(self, claims=None, header=None, proof_change=None):
        data = fixture(); proof = data['proof']
        proof.update(proof_change or {})
        issued = int(datetime.fromisoformat('2026-10-05T18:15:00+00:00').timestamp())
        values = dict(iss=reuse.ISSUER, aud=reuse.audience(proof), repository=REPO, repository_id='77',
                      event_name='pull_request', ref='refs/pull/42/merge', base_ref='master', sha=CANDIDATE,
                      workflow_sha=CANDIDATE, workflow_ref=f'{REPO}/{reuse.WORKFLOW}@refs/pull/42/merge',
                      run_id='123', run_attempt='2', check_run_id='789', runner_environment='github-hosted',
                      iat=issued, nbf=issued - 5, exp=issued + 300)
        values.update(claims or {})
        unsigned = '.'.join(self.encode(json.dumps(item, sort_keys=True).encode()) for item in
                            ({'alg': 'RS256', 'kid': 'test-key', **(header or {})}, values))
        signature = subprocess.check_output(['openssl', 'dgst', '-sha256', '-sign', str(self.private)],
                                            input=unsigned.encode(), stderr=subprocess.DEVNULL)
        proof['identity_token'] = unsigned + '.' + self.encode(signature)
        return data

    def verify(self, data, keys=None):
        return reuse.authenticated(data['proof'], keys or self.keys, data['run'], data['jobs'][0])

    def test_signed_receipt_binds_exact_workflow_checkout_run_attempt_and_job(self):
        self.assertTrue(self.verify(self.receipt()))
        # Archival verification accepts a receipt issued during its completed job,
        # even after expiration; there is no exchange/renewal against a provider.
        self.assertTrue(self.verify(self.receipt(claims={'base_ref': 'refs/heads/master'})))

    def test_wrong_signed_identity_claims_reject_self_asserted_artifact(self):
        for name, value in [('iss', 'https://other'), ('aud', 'other'), ('repository', 'attacker/fork'),
                            ('repository_id', '78'), ('event_name', 'push'), ('ref', 'refs/pull/43/merge'),
                            ('base_ref', 'other'), ('sha', HEAD), ('workflow_sha', HEAD),
                            ('workflow_ref', f'{REPO}/{reuse.WORKFLOW}@refs/heads/old'),
                            ('run_id', '456'), ('run_attempt', '1'), ('check_run_id', '790'),
                            ('runner_environment', 'self-hosted')]:
            with self.subTest(claim=name):
                self.assertFalse(self.verify(self.receipt(claims={name: value})))

    def test_audience_prevents_replacing_proof_metadata_materials_or_identity(self):
        for key, value in [('tree', HEAD), ('input_digest', '0' * 64), ('materials', {}),
                           ('parents', [HEAD, BASE]), ('checkout', HEAD)]:
            data = self.receipt(); data['proof'][key] = value
            self.assertFalse(self.verify(data))

    def test_wrong_signature_algorithm_or_unavailable_historical_key_rejects(self):
        for header in (dict(alg='none'), dict(alg='HS256'), dict(kid='unknown')):
            self.assertFalse(self.verify(self.receipt(header=header)))
        data = self.receipt(); token = data['proof']['identity_token']
        data['proof']['identity_token'] = token.rsplit('.', 1)[0] + '.' + self.encode(b'wrong-signature')
        self.assertFalse(self.verify(data))
        self.assertFalse(self.verify(self.receipt(), keys={'keys': []}))

    def test_receipt_must_have_been_valid_within_source_job_window(self):
        issued = int(datetime.fromisoformat('2026-10-05T18:15:00+00:00').timestamp())
        for changes in (dict(iat=issued - 3600), dict(iat=issued + 3600), dict(exp=issued),
                        dict(nbf=issued + 1), dict(exp=issued + 601), dict(iat=str(issued))):
            self.assertFalse(self.verify(self.receipt(claims=changes)))


if __name__ == '__main__':
    unittest.main()
