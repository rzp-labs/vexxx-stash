#!/usr/bin/env python3
"""Reuse packaging *evidence* on an unchanged, same-repository PR merge landing.

Tests and landed identity validation still execute. No image is promoted, and
release/manual events always build their own image. Unavailable proof falls back
to the ordinary build; variable values and API errors are never printed.
"""
import base64
from datetime import datetime, timezone
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request
import zipfile

from image_publication_policy import checkout_identity
from reuse_master_validation import WORKFLOW, DOMAINS

DOCKERFILE = 'docker/build/x86_64/Dockerfile'
VARIABLES = ('POSTHOG_CLI_PROJECT_ID', 'POSTHOG_CLI_HOST',
             'VITE_PUBLIC_POSTHOG_PROJECT_TOKEN', 'VITE_PUBLIC_POSTHOG_HOST')
SHA = re.compile(r'[0-9a-f]{40}')
DIGEST = re.compile(r'[0-9a-f]{64}')
LIMIT = 65536
ISSUER = 'https://token.actions.githubusercontent.com'
AUDIENCE = 'urn:rzp-labs:pr-packaging:'


def digest(value):
    return hashlib.sha256(value).hexdigest()


def audience(proof):
    unsigned = {key: value for key, value in proof.items() if key != 'identity_token'}
    return AUDIENCE + digest(json.dumps(unsigned, sort_keys=True, separators=(',', ':')).encode())


def unbase64(value):
    return base64.b64decode(value + '=' * (-len(value) % 4), altchars=b'-_', validate=True)


def der(tag, value):
    length = len(value)
    size = length.to_bytes((length.bit_length() + 7) // 8 or 1, 'big')
    return bytes([tag]) + (bytes([length]) if length < 128 else bytes([128 + len(size)]) + size) + value


def rsa_public_key(key):
    # Encode JWK public integers as RSA SubjectPublicKeyInfo. Cryptographic
    # verification is delegated to OpenSSL, not implemented here.
    def integer(encoded):
        value = unbase64(encoded).lstrip(b'\0') or b'\0'
        return der(2, (b'\0' if value[0] & 128 else b'') + value)
    rsa = der(48, integer(key['n']) + integer(key['e']))
    spki = der(48, bytes.fromhex('300d06092a864886f70d0101010500') + der(3, b'\0' + rsa))
    encoded = base64.b64encode(spki).decode()
    return ('-----BEGIN PUBLIC KEY-----\n' + '\n'.join(encoded[i:i + 64] for i in range(0, len(encoded), 64))
            + '\n-----END PUBLIC KEY-----\n')


def authenticated(proof, keys, run, job):
    """Verify a historical GitHub receipt, never exchange an expired JWT.

    Audience binds the whole proof. Signed run/job and workflow/checkout claims
    independently establish which immutable workflow actually issued it. Tokens
    must have been valid during the completed source job; key rotation is closed.
    """
    token = proof['identity_token']
    if not isinstance(token, str) or len(token) > 16384:
        return False
    head, body, signature = token.split('.')
    header, claims = json.loads(unbase64(head)), json.loads(unbase64(body))
    matching = [key for key in keys['keys'] if key.get('kid') == header.get('kid')
                and key.get('kty') == 'RSA' and key.get('alg') == 'RS256' and key.get('use') == 'sig']
    if header.get('alg') != 'RS256' or len(matching) != 1:
        return False
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        (root / 'key.pem').write_text(rsa_public_key(matching[0]))
        (root / 'signature').write_bytes(unbase64(signature))
        result = subprocess.run(['openssl', 'dgst', '-sha256', '-verify', str(root / 'key.pem'),
                                 '-signature', str(root / 'signature')], input=(head + '.' + body).encode(),
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
    if result.returncode:
        return False
    pr, repo, candidate = proof['pr'], proof['repository'], proof['checkout']
    expected = dict(iss=ISSUER, aud=audience(proof), repository=repo,
                    repository_id=str(run['repository']['id']), event_name='pull_request',
                    ref=f'refs/pull/{pr}/merge', sha=candidate, workflow_sha=candidate,
                    workflow_ref=f'{repo}/{WORKFLOW}@refs/pull/{pr}/merge',
                    run_id=str(run['id']), run_attempt=str(run['run_attempt']),
                    check_run_id=job['check_run_url'].rsplit('/', 1)[-1], runner_environment='github-hosted')
    if any(claims.get(key) != value for key, value in expected.items()):
        return False
    if claims.get('base_ref') not in ('master', 'refs/heads/master'):
        return False
    times = [claims.get(key) for key in ('iat', 'nbf', 'exp')]
    if any(type(value) is not int for value in times):
        return False
    issued, valid_from, expires = times
    start = datetime.fromisoformat(job['started_at'].replace('Z', '+00:00')).timestamp()
    finish = datetime.fromisoformat(job['completed_at'].replace('Z', '+00:00')).timestamp()
    return start <= issued <= finish and valid_from <= issued < expires <= issued + 600


def public_keys():
    # This fixed GitHub issuer endpoint receives no repository/OIDC bearer token.
    with urllib.request.urlopen(ISSUER + '/.well-known/jwks', timeout=10) as response:
        return json.loads(response.read(65537))


def identity_receipt(proof):
    e = os.environ
    url = e['ACTIONS_ID_TOKEN_REQUEST_URL'] + '&' + urllib.parse.urlencode({'audience': audience(proof)})
    if urllib.parse.urlsplit(url).scheme != 'https':
        raise ValueError('insecure receipt endpoint')
    req = urllib.request.Request(url, headers={'Authorization': 'Bearer ' + e['ACTIONS_ID_TOKEN_REQUEST_TOKEN']})
    with urllib.request.build_opener(TokenFreeRedirect()).open(req, timeout=10) as response:
        return json.loads(response.read(32769))['value']


def input_digest(tree, workflow, variables):
    # Commit identity is intentionally different, not promoted. Only validation
    # ci-<commit>/commit-date/ref metadata is normalized; publication is excluded.
    inputs = dict(tree=tree, workflow=digest(workflow), variables=variables,
                  platform='linux/amd64', publication=False,
                  identity='validation commit / ci-commit / commit date / validation ref')
    return digest(json.dumps(inputs, sort_keys=True, separators=(',', ':')).encode())


def reference(value):
    if not re.fullmatch(r'[A-Za-z0-9_./:@-]+', value) or '$' in value:
        raise ValueError('unsupported image reference')
    value = value.removeprefix('docker.io/').removeprefix('library/')
    if ':' not in value.rsplit('/', 1)[-1] and '@' not in value:
        value += ':latest'
    return value


def bases(dockerfile):
    aliases, result = set(), set()
    for line in dockerfile.splitlines():
        if not line.lstrip().upper().startswith('FROM '):
            continue
        parts = line.split()
        if len(parts) not in (2, 4) or (len(parts) == 4 and parts[2].upper() != 'AS'):
            raise ValueError('unsupported FROM')
        if parts[1] not in aliases:
            result.add(reference(parts[1]))
        if len(parts) == 4:
            aliases.add(parts[3])
    if not result:
        raise ValueError('missing base inputs')
    return result


def materials(metadata, dockerfile):
    provenance = metadata.get('buildx.build.provenance', {})
    if isinstance(provenance, str):
        provenance = json.loads(provenance)
    result = {}
    for item in provenance.get('materials', []):
        uri = item['uri']
        if not uri.startswith('pkg:docker/'):
            raise ValueError('unsupported build material')
        parsed = urllib.parse.urlsplit(uri)
        name, version = urllib.parse.unquote(parsed.path.removeprefix('docker/')).rsplit('@', 1)
        query = urllib.parse.parse_qs(parsed.query)
        if query.get('platform', ['linux/amd64']) != ['linux/amd64']:
            raise ValueError('unsupported material platform')
        ref = reference(name + ('@' if version.startswith('sha256:') else ':') + version)
        sha = item['digest']['sha256']
        if not DIGEST.fullmatch(sha) or ref in result:
            raise ValueError('invalid material')
        result[ref] = sha
    if not bases(dockerfile) <= result.keys():
        raise ValueError('incomplete base provenance')
    return result


def current_material(ref, expected):
    # Registry reads only. BuildKit metadata identifies the actual source build's
    # materials; mutable tags must still resolve to those digests on landing.
    raw = subprocess.check_output(['docker', 'buildx', 'imagetools', 'inspect', ref, '--raw'],
                                  timeout=30, stderr=subprocess.DEVNULL)
    if len(raw) > 1024 * 1024:
        return False
    document = json.loads(raw)
    digests = {digest(raw)}
    digests.add(digest(raw.rstrip(b'\n')))
    for item in document.get('manifests', []):
        platform = item.get('platform', {})
        if platform.get('os') == 'linux' and platform.get('architecture') == 'amd64':
            digests.add(item['digest'].removeprefix('sha256:'))
    return expected in digests


def step_passed(job, name):
    matches = [step for step in job.get('steps', []) if step.get('name') == name]
    return (len(matches) == 1 and matches[0].get('status') == 'completed'
            and matches[0].get('conclusion') == 'success')


def trusted_run(run, repo, head, workflow_id):
    return (run.get('event') == 'pull_request' and run.get('head_sha') == head
            and run.get('repository', {}).get('full_name') == repo
            and run.get('head_repository', {}).get('full_name') == repo
            and run.get('path') == WORKFLOW and run.get('workflow_id') == workflow_id
            and run.get('status') == 'completed' and run.get('conclusion') == 'success'
            and type(run.get('id')) is int and run['id'] > 0
            and type(run.get('run_attempt')) is int and run['run_attempt'] > 0)


def unpack(raw, archive_digest):
    if len(raw) > LIMIT or archive_digest != 'sha256:' + digest(raw):
        raise ValueError('invalid evidence archive')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        infos = archive.infolist()
        if len(infos) != 1 or infos[0].filename != 'pr-packaging.json' or infos[0].file_size > LIMIT:
            raise ValueError('invalid evidence inventory')
        return json.loads(archive.read(infos[0]))


def find_source(api, download, check_material, verify_identity, plan, parents, repo, workflow_sha, workflow_bytes, inputs):
    if (workflow_sha != plan['checkout'] or len(parents) != 2
            or plan['base'] != parents[0] or plan['head'] != plan['checkout']):
        return None
    workflow = api('/actions/workflows/docker-publish.yml')
    if workflow.get('path') != WORKFLOW:
        return None
    prs = api('/commits/' + plan['checkout'] + '/pulls?per_page=100')
    if len(prs) >= 100:
        return None
    for linked in prs:
        pr = api('/pulls/' + str(linked['number']))
        if not (pr.get('merged') is True and pr.get('draft') is False
                and pr.get('merge_commit_sha') == plan['checkout']
                and pr.get('base', {}).get('ref') == 'master'
                and pr['base'].get('repo', {}).get('full_name') == repo
                and pr.get('head', {}).get('repo', {}).get('full_name') == repo
                and [pr['base']['sha'], pr['head']['sha']] == parents):
            continue
        query = urllib.parse.urlencode(dict(event='pull_request', head_sha=parents[1], status='success', per_page=20))
        runs = api('/actions/workflows/docker-publish.yml/runs?' + query)
        for run in runs['workflow_runs']:
            if not trusted_run(run, repo, parents[1], workflow['id']):
                continue
            path = f"/actions/runs/{run['id']}"
            inventory = api(path + f"/attempts/{run['run_attempt']}/jobs?per_page=100")
            jobs = [job for job in inventory['jobs'] if job.get('name') == 'ci-required']
            if inventory.get('total_count') != len(inventory['jobs']) or len(jobs) != 1:
                continue
            job = jobs[0]
            names = ['Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
                     'Verify exact-master validation evidence', 'Verify PR packaging evidence',
                     'Build Linux amd64 image locally', 'Smoke test container', 'Verify every selected obligation',
                     'Record PR packaging evidence', 'Save PR packaging evidence']
            names += [name for domain, steps in DOMAINS.items() if plan[domain] for name in steps]
            if not (job.get('head_sha') == parents[1] and job.get('run_id') == run['id']
                    and job.get('run_attempt') == run['run_attempt'] and job.get('status') == 'completed'
                    and job.get('conclusion') == 'success' and all(step_passed(job, name) for name in names)):
                continue
            inventory = api(path + '/artifacts?per_page=100')
            matches = [a for a in inventory['artifacts'] if a.get('name') == f"pr-packaging-{run['run_attempt']}"
                       and a.get('expired') is False and a.get('workflow_run', {}).get('id') == run['id']]
            if inventory.get('total_count') != len(inventory['artifacts']) or len(matches) != 1:
                continue
            artifact = matches[0]
            proof = unpack(download(artifact['id']), artifact['digest'])
            if not verify_identity(proof, run, job):
                continue  # Artifact claims alone never establish executed workflow provenance.
            candidate = proof['checkout']
            if not SHA.fullmatch(candidate):
                continue
            commit = api('/commits/' + candidate)
            content = api('/contents/' + WORKFLOW + '?ref=' + candidate)
            if not (proof.get('schema') == 1 and proof.get('run') == run['id']
                    and proof.get('attempt') == run['run_attempt'] and proof.get('pr') == pr['number']
                    and proof.get('repository') == repo and proof.get('parents') == parents
                    and proof.get('tree') == plan['tree'] and proof.get('input_digest') == inputs
                    and proof.get('workflow_digest') == digest(workflow_bytes)
                    and proof.get('workflow_sha') == candidate
                    and proof.get('domains') == {key: plan[key] for key in DOMAINS}
                    and proof.get('version') == 'ci-' + candidate[:12]
                    and proof.get('publish') is False and proof.get('build_image') is True
                    and commit.get('sha') == candidate
                    and commit['commit']['tree']['sha'] == plan['tree']
                    and [p['sha'] for p in commit['parents']] == parents
                    and proof.get('build_date') == datetime.fromisoformat(
                        commit['commit']['committer']['date'].replace('Z', '+00:00')).astimezone(
                            timezone.utc).strftime('%Y-%m-%d %H:%M:%S')
                    and content.get('encoding') == 'base64'
                    and base64.b64decode(content['content'], validate=False) == workflow_bytes):
                continue
            expected = proof.get('materials', {})
            if not 1 <= len(expected) <= 16 or not bases(Path(DOCKERFILE).read_text()) <= expected.keys() or not all(
                    DIGEST.fullmatch(value) and check_material(reference(ref), value) for ref, value in expected.items()):
                continue
            fresh = api(path)
            if not trusted_run(fresh, repo, parents[1], workflow['id']) or fresh['run_attempt'] != run['run_attempt']:
                continue
            return dict(run=run['id'], attempt=run['run_attempt'], candidate=candidate, pr=pr['number'],
                        landed=plan['checkout'], tree=plan['tree'], workflow_digest=digest(workflow_bytes),
                        input_digest=inputs, materials=expected)
    return None


class TokenFreeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urllib.parse.urlsplit(newurl).scheme != 'https':
            raise ValueError('insecure artifact redirect')
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        redirected.remove_header('Authorization')
        return redirected


def request(path, binary=False):
    e = os.environ
    req = urllib.request.Request('https://api.github.com/repos/' + e['GITHUB_REPOSITORY'] + path,
        headers={'Authorization': 'Bearer ' + e['GH_TOKEN'], 'Accept': 'application/vnd.github+json',
                 'X-GitHub-Api-Version': '2022-11-28'})
    with urllib.request.build_opener(TokenFreeRedirect()).open(req, timeout=10) as response:
        raw = response.read(LIMIT + 1 if binary else 2 * 1024 * 1024)
    return raw if binary else json.loads(raw)


def main():
    e = os.environ
    plan = json.loads(Path(e['VALIDATION_PLAN_PATH']).read_text())
    # Fresh local identity validation is mandatory even when no prior image runs.
    tree = checkout_identity(Path.cwd(), e['GITHUB_SHA'])
    if tree != plan['tree'] or plan['checkout'] != e['GITHUB_SHA']:
        raise ValueError('planner identity mismatch')
    parents = subprocess.check_output(['git', 'rev-list', '--parents', '-n', '1', e['GITHUB_SHA']],
                                     text=True, timeout=60).split()[1:]
    workflow = Path(WORKFLOW).read_bytes()
    inputs = input_digest(tree, workflow, {key: e.get(key, '') for key in VARIABLES})
    if len(sys.argv) > 1 and sys.argv[1] == 'record':
        # Called only after the required gate and real PR build/smoke succeeded.
        proof = dict(schema=1, run=int(e['GITHUB_RUN_ID']), attempt=int(e['GITHUB_RUN_ATTEMPT']),
                     repository=e['GITHUB_REPOSITORY'], pr=int(e['PR_NUMBER']), checkout=plan['checkout'],
                     tree=tree, parents=parents, workflow_sha=e['GITHUB_WORKFLOW_SHA'],
                     workflow_digest=digest(workflow), input_digest=inputs,
                     domains={key: plan[key] for key in DOMAINS}, version=plan['version'],
                     build_date=plan['build_date'],
                     publish=plan['publish'], build_image=plan['build_image'])
        recorded = False
        try:
            proof['materials'] = materials(json.loads(e['BUILD_METADATA']), Path(DOCKERFILE).read_text())
            proof['identity_token'] = identity_receipt(proof)
            Path(e['PACKAGING_EVIDENCE_PATH']).write_text(json.dumps(proof, sort_keys=True))
            recorded = True
        except (OSError, ValueError, KeyError, TypeError, AttributeError, http.client.HTTPException):
            pass  # Successful validation without provenance remains non-reusable.
        with Path(e['GITHUB_OUTPUT']).open('a') as output:
            output.write('recorded=' + str(recorded).lower() + '\n')
        with Path(e['GITHUB_STEP_SUMMARY']).open('a') as summary:
            summary.write('\n\nPR packaging proof recorded.' if recorded else
                          '\n\nNo reusable PR proof: signed workflow receipt or actual base provenance unavailable. Landing will rebuild.')
        return
    proof = None
    if (e['GITHUB_EVENT_NAME'] == 'push' and e['GITHUB_REF_TYPE'] == 'branch'
            and e['GITHUB_REF_NAME'] == 'master' and plan['build_image'] and not plan['publish']):
        try:
            proof = find_source(request, lambda ident: request(f'/actions/artifacts/{ident}/zip', True),
                                current_material, lambda proof, run, job: authenticated(proof, public_keys(), run, job),
                                plan, parents, e['GITHUB_REPOSITORY'],
                                e['GITHUB_WORKFLOW_SHA'], workflow, inputs)
        except (OSError, ValueError, KeyError, TypeError, AttributeError, zipfile.BadZipFile,
                subprocess.SubprocessError, http.client.HTTPException, RuntimeError):
            pass
    outputs = dict(reused=bool(proof), execute_image=plan['build_image'] and not proof,
                   landed=plan['checkout'], tree=tree, workflow_digest=digest(workflow), input_digest=inputs,
                   source_run='', source_attempt='', candidate='')
    if proof:
        outputs.update(source_run=str(proof['run']), source_attempt=str(proof['attempt']), candidate=proof['candidate'])
    with Path(e['GITHUB_OUTPUT']).open('a') as output:
        for key, value in outputs.items():
            output.write(key + '=' + (str(value).lower() if isinstance(value, bool) else str(value)) + '\n')
    reason = ('No complete equivalent PR packaging proof; selected image validation executes normally.' if not proof else
              f"Reused packaging validation from PR #{proof['pr']}: "
              f"https://github.com/{e['GITHUB_REPOSITORY']}/actions/runs/{proof['run']}/attempts/{proof['attempt']}.\n"
              f"Candidate `{proof['candidate']}`; fresh landed identity `{proof['landed']}`; tree `{tree}`.\n"
              f"Workflow SHA256 `{digest(workflow)}`; normalized build-input SHA256 `{inputs}`.\n"
              'GitHub-signed source workflow/job receipt, ordered parents, selected coverage and resolved base materials verified. Tests execute on master. '
              'No landed image was built or promoted; release image/smoke obligations remain distinct.')
    with Path(e['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('\n### Packaging validation\n\n' + reason + '\n')


if __name__ == '__main__':
    main()
