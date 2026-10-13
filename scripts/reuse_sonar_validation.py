#!/usr/bin/env python3
"""Bounded GitHub-native test/report reuse; full-tree Sonar clearance is distinct.

Artifact claims never replace immutable Git inputs and executed job steps. Missing
reuse evidence executes checks normally. Readiness instead fails closed without
fresh clearance for the current head, base, target and actual merge-result tree.
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
import time
import urllib.parse
import urllib.request
import zipfile

from reuse_pr_packaging import request, TokenFreeRedirect, VARIABLES, step_passed
from reuse_master_validation import WORKFLOW, main as reuse_master
from sonar_scan_policy import COVERAGE, verify_current_pr, parse_properties

SHA = re.compile(r'[0-9a-f]{40}')
LIMIT = 16 * 1024 * 1024
DOMAINS = {
    'backend': ('Generate backend and login locales', 'Go unit and integration tests',
                'Production backend compilation'),
    'frontend': ('Install frozen frontend dependencies and generate operations',
                 'Frontend tests and type check', 'Production frontend compilation'),
    'python': ('Python regression tests',),
}
REPORTS = {'backend': {'go-unit.out': 'dist/coverage/go-unit.out',
                       'go-integration.out': 'dist/coverage/go-integration.out'},
           'frontend': {'lcov.info': 'dist/coverage/frontend/lcov.info'},
           'python': {'python.xml': 'dist/coverage/python.xml'}}
ARTIFACTS = {'backend': 'go-coverage', 'frontend': 'frontend-coverage', 'python': 'python-coverage'}
RETAIN = {'backend': 'Retain Go coverage from this validation',
          'frontend': 'Retain frontend coverage from this validation',
          'python': 'Retain Python coverage from this validation'}
REUSE_STEP = 'Verify exact-master validation evidence'
PROJECT_KEY = 'rzp-labs_vexxx-stash'
ERRORS = (OSError, ValueError, KeyError, TypeError, AttributeError,
          zipfile.BadZipFile, subprocess.SubprocessError, http.client.HTTPException)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def fingerprint(entries, variables):
    """Conservatively bind all tracked blobs and modes, except scanner properties."""
    selected = []
    for entry in entries:
        if entry['path'] == 'sonar-project.properties':
            continue
        if entry['type'] not in ('blob', 'commit') or not SHA.fullmatch(entry['sha']):
            raise ValueError('unsupported source inventory')
        selected.append((entry['path'], entry['mode'], entry['type'], entry['sha']))
    if len({row[0] for row in selected}) != len(selected):
        raise ValueError('duplicate tracked input')
    return digest(canonical({'files': sorted(selected), 'variables': variables}))


def local_inputs(revision, variables):
    raw = subprocess.check_output(['git', 'ls-tree', '-r', '-z', revision], timeout=60)
    entries = []
    for row in raw.split(b'\0'):
        if not row:
            continue
        metadata, path = row.split(b'\t', 1)
        mode, kind, sha = metadata.decode().split()
        entries.append(dict(path=path.decode(), mode=mode, type=kind, sha=sha))
    return fingerprint(entries, variables)


def remote_inputs(api, revision, variables):
    commit = api('/git/commits/' + revision)
    if commit.get('sha') != revision or not SHA.fullmatch(commit['tree']['sha']):
        raise ValueError('invalid donor commit')
    inventory = api('/git/trees/' + commit['tree']['sha'] + '?recursive=1')
    if inventory.get('truncated') is not False or inventory.get('sha') != commit['tree']['sha']:
        raise ValueError('incomplete donor tree')
    entries = [row for row in inventory['tree'] if row['type'] != 'tree']
    return fingerprint(entries, variables), commit['tree']['sha']


def content(api, path, revision):
    data = api('/contents/' + path + '?ref=' + revision)
    if data.get('encoding') != 'base64':
        raise ValueError('missing donor file')
    return base64.b64decode(data['content'], validate=False)


def config(raw):
    result = parse_properties(raw.decode())
    result.pop('sonar.exclusions', None)
    return result


def unpack(raw, expected_digest, names, maximum=LIMIT):
    if len(raw) > maximum or expected_digest != 'sha256:' + digest(raw):
        raise ValueError('invalid artifact digest/size')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        infos = archive.infolist()
        if (len(infos) != len(names) or {item.filename for item in infos} != set(names)
                or any(item.is_dir() or item.file_size <= 0 for item in infos)
                or sum(item.file_size for item in infos) > maximum):
            raise ValueError('invalid artifact inventory')
        return {item.filename: archive.read(item) for item in infos}


def download(ident):
    # Reuse the token-stripping redirect policy, with a report-sized bounded read.
    if type(ident) is not int or ident <= 0:
        raise ValueError('invalid artifact identity')
    e = os.environ
    req = urllib.request.Request('https://api.github.com/repos/' + e['GITHUB_REPOSITORY'] +
        f'/actions/artifacts/{ident}/zip', headers={'Authorization': 'Bearer ' + e['GH_TOKEN'],
        'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'})
    with urllib.request.build_opener(TokenFreeRedirect()).open(req, timeout=10) as response:
        return response.read(LIMIT + 1)


def artifact_inventory(api, run, job, name):
    inventory = api(f"/actions/runs/{run['id']}/artifacts?per_page=100")
    if inventory.get('total_count') != len(inventory['artifacts']):
        raise ValueError('incomplete artifact inventory')
    matches = [a for a in inventory['artifacts'] if a.get('name') == name
               and a.get('expired') is False and a.get('workflow_run', {}).get('id') == run['id']
               and timestamp(job['started_at']) <= timestamp(a.get('created_at', '')) <= timestamp(job['completed_at'])]
    if len(matches) != 1:
        raise ValueError('missing, stale or ambiguous artifact')
    return matches[0]


def evidence_artifact(api, download, run, job, name, filename):
    artifact = artifact_inventory(api, run, job, name)
    raw = unpack(download(artifact['id']), artifact['digest'], {filename}, 65536)[filename]
    return json.loads(raw)


def trusted_run(run, repository, workflow_id, pr_number):
    same_pr = (run.get('event') == 'pull_request'
               and any(p.get('number') == pr_number for p in run.get('pull_requests', [])))
    master = run.get('event') == 'push' and run.get('head_branch') == 'master'
    return (same_pr or master) and (
        run.get('repository', {}).get('full_name') == repository
        and run.get('head_repository', {}).get('full_name') == repository
        and run.get('path') == WORKFLOW and run.get('workflow_id') == workflow_id
        and run.get('status') == 'completed' and run.get('conclusion') in ('success', 'failure')
        and SHA.fullmatch(run.get('head_sha', ''))
        and type(run.get('id')) is int and run['id'] > 0
        and type(run.get('run_attempt')) is int and run['run_attempt'] > 0)


def source_job(api, run):
    inventory = api(f"/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")
    if inventory.get('total_count') != len(inventory['jobs']):
        raise ValueError('incomplete job inventory')
    jobs = [job for job in inventory['jobs'] if job.get('name') == 'ci-required']
    if len(jobs) != 1:
        raise ValueError('missing required job')
    job = jobs[0]
    if (job.get('head_sha') != run['head_sha'] or job.get('run_id') != run['id']
            or job.get('run_attempt') != run['run_attempt'] or job.get('status') != 'completed'
            or job.get('conclusion') not in ('success', 'failure')):
        raise ValueError('stale donor job')
    for name in ('Publication policy and packaging tests',
                 'Classify changed inputs and deliberate publication', REUSE_STEP,
                 'Verify every selected obligation', 'Record executed native validation',
                 'Save executed validation evidence'):
        if not step_passed(job, name):
            raise ValueError('incomplete native validation')
    return job


def eligible_scan_donor(run, job):
    """Accept the deliberately pending draft packaging guard, never other failures."""
    if run.get('conclusion') == 'success' and job.get('conclusion') == 'success':
        return all(step.get('status') == 'completed' and step.get('conclusion') in ('success', 'skipped')
                   for step in job.get('steps', []))
    if run.get('conclusion') != 'failure' or job.get('conclusion') != 'failure':
        return False
    steps = job.get('steps', [])
    failed = [step for step in steps if step.get('conclusion') not in ('success', 'skipped')]
    return (len(failed) == 1 and failed[0].get('name') == 'Require selected packaging validation'
            and failed[0].get('status') == 'completed' and failed[0].get('conclusion') == 'failure'
            and all(step.get('status') == 'completed' for step in steps))


def domain_covered(job, domain, build_image):
    if type(build_image) is not bool:
        return False
    names = DOMAINS[domain]
    if domain == 'python':
        return all(step_passed(job, name) for name in names)
    if not all(step_passed(job, name) for name in names[:-1]):
        return False
    if not build_image:
        return step_passed(job, names[-1])
    host = [step for step in job.get('steps', []) if step.get('name') == names[-1]]
    return (len(host) == 1 and host[0].get('status') == 'completed'
            and host[0].get('conclusion') == 'skipped'
            and step_passed(job, 'Build Linux amd64 image locally')
            and step_passed(job, 'Smoke test container'))


def sonar_check_link(value, pr_number, project_key=PROJECT_KEY):
    """The native app's project/PR URL binds checks with no GitHub association."""
    try:
        url = urllib.parse.urlsplit(value)
        query = urllib.parse.parse_qs(url.query, keep_blank_values=True, strict_parsing=True)
    except (TypeError, ValueError, AttributeError):
        return False
    return (url.scheme == 'https' and url.netloc == 'sonarcloud.io'
            and url.path == '/dashboard' and not url.fragment
            and all(len(values) == 1 for values in query.values())
            and query.get('id') == [project_key]
            and query.get('pullRequest') == [str(pr_number)])


def native_check(check, head, pr_number, started):
    return (type(check.get('id')) is int and check['id'] > 0
            and check.get('name') == 'SonarCloud Code Analysis'
            and check.get('app', {}).get('id') == 12526
            and check.get('head_sha') == head and check.get('status') == 'completed'
            and check.get('conclusion') == 'success'
            and sonar_check_link(check.get('details_url'), pr_number)
            and (not check.get('pull_requests') or any(
                p.get('number') == pr_number for p in check['pull_requests']))
            and timestamp(check['started_at']) >= timestamp(started)
            and timestamp(check['completed_at']) >= timestamp(check['started_at']))


def timestamp(value):
    result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('timestamp without timezone')
    return result


def find_check(api, identity, pr_number, started, check_id=None):
    data = api('/commits/' + identity['head'] + '/check-runs?per_page=100&filter=all')
    if data.get('total_count') != len(data['check_runs']):
        raise ValueError('incomplete check inventory')
    matches = [c for c in data['check_runs'] if native_check(c, identity['head'], pr_number, started)
               and (check_id is None or c['id'] == check_id)]
    if len(matches) != 1:
        raise ValueError('no unique fresh native Sonar clearance')
    return matches[0]


def exact_clearance(api, download, run, job, identity, plan, workflow_digest, config_digest):
    for name in ('Submit analysis to the existing Sonar project', 'Record current native Sonar clearance',
                 'Save current Sonar clearance'):
        if not step_passed(job, name):
            raise ValueError('scan was not cleared')
    proof = evidence_artifact(api, download, run, job, f"sonar-clearance-{run['run_attempt']}",
                              'sonar-clearance.json')
    expected = dict(schema=1, repository=run['repository']['full_name'], run=run['id'],
                    attempt=run['run_attempt'], pr=plan['pr'], workflow_digest=workflow_digest,
                    config_digest=config_digest, analyzed_revision=identity['head'],
                    quality_gate='OK', head=identity['head'], base=identity['base'],
                    target=identity['target'], tree=identity['tree'])
    if any(proof.get(key) != value for key, value in expected.items()):
        raise ValueError('clearance identity mismatch')
    if not re.fullmatch(r'[A-Za-z0-9_-]+', proof.get('ce_task_id', '')):
        raise ValueError('missing analysis task')
    check = find_check(api, identity, plan['pr'], proof['scan_started_at'], proof['sonar_check_id'])
    if check['completed_at'] != proof['check_completed_at']:
        raise ValueError('changed Sonar check')
    return dict(check_id=check['id'], scan_started_at=proof['scan_started_at'],
                check_completed_at=check['completed_at'])


def find_source(api, download, plan, identity, repository, current_run, workflow,
                properties, variables, required, inputs, ready=False):
    workflow_data = api('/actions/workflows/docker-publish.yml')
    if workflow_data.get('path') != WORKFLOW:
        raise ValueError('wrong source workflow')
    query = urllib.parse.urlencode({'per_page': 30})
    # One bounded page, with each donor independently validated. A truncated list
    # merely limits candidate discovery; inventories within each donor are closed.
    runs = api('/actions/workflows/docker-publish.yml/runs?' + query)['workflow_runs']
    best, clearance = None, None
    needed_reports = {d for d, (field, _) in COVERAGE.items() if plan.get(field)}
    for listed in runs:
        if str(listed.get('id')) == str(current_run):
            continue
        if not trusted_run(listed, repository, workflow_data['id'], plan['pr']):
            continue
        try:
            run = api(f"/actions/runs/{listed['id']}")
            if not trusted_run(run, repository, workflow_data['id'], plan['pr']) or (
                    run['run_attempt'] != listed['run_attempt'] or run['head_sha'] != listed['head_sha']):
                continue
            job = source_job(api, run)
            proof = evidence_artifact(api, download, run, job, f"validation-evidence-{run['run_attempt']}",
                                      'validation-evidence.json')
            candidate = proof['checkout']
            if not SHA.fullmatch(candidate):
                continue
            donor_inputs, donor_tree = remote_inputs(api, candidate, variables)
            head_inputs, head_tree = remote_inputs(api, run['head_sha'], variables)
            if (donor_inputs != inputs or head_inputs != inputs or donor_tree != head_tree
                    or content(api, WORKFLOW, candidate) != workflow
                    or content(api, WORKFLOW, run['head_sha']) != workflow
                    or config(content(api, 'sonar-project.properties', candidate)) != config(properties)):
                continue
            expected = dict(schema=1, repository=repository, run=run['id'], attempt=run['run_attempt'],
                            tree=donor_tree, input_digest=inputs, workflow_digest=digest(workflow),
                            variables_digest=digest(canonical(variables)))
            if any(proof.get(key) != value for key, value in expected.items()):
                continue
            commit = api('/git/commits/' + candidate)
            if run['event'] == 'pull_request':
                if (proof.get('pr') != plan['pr'] or proof.get('head') != run['head_sha']
                        or [p['sha'] for p in commit['parents']] != [proof.get('base'), run['head_sha']]):
                    continue
            elif candidate != run['head_sha']:
                continue
            if type(proof.get('build_image')) is not bool:
                continue
            proven = {d for d in DOMAINS if proof.get('domains', {}).get(d) is True
                      and domain_covered(job, d, proof['build_image'])}
            covered = proven & required
            report_domains = proven & needed_reports
            pending_clearance = None
            if ready and eligible_scan_donor(run, job) and run['head_sha'] == identity['head']:
                try:
                    checked = exact_clearance(api, download, run, job, identity, plan,
                                               digest(workflow), digest(properties))
                    pending_clearance = dict(run=run['id'], attempt=run['run_attempt'], revision=candidate,
                                     tree=donor_tree, **checked)
                except ERRORS:
                    pass
            reports = {}
            for domain in report_domains:
                if not step_passed(job, RETAIN[domain]):
                    raise ValueError('report-producing upload did not succeed')
                artifact = artifact_inventory(api, run, job, ARTIFACTS[domain])
                values = unpack(download(artifact['id']), artifact['digest'], REPORTS[domain])
                reports.update({REPORTS[domain][name]: raw for name, raw in values.items()})
            fresh = api(f"/actions/runs/{run['id']}")
            if not trusted_run(fresh, repository, workflow_data['id'], plan['pr']) or (
                    fresh['run_attempt'] != run['run_attempt'] or fresh['head_sha'] != run['head_sha']):
                continue
            if pending_clearance and eligible_scan_donor(fresh, job):
                clearance = pending_clearance
            result = dict(run=run['id'], attempt=run['run_attempt'], revision=candidate, head=run['head_sha'],
                          tree=donor_tree, covered=covered, reports=reports, report_domains=report_domains, check_id='')
            if best is None or (len(covered), len(report_domains)) > (
                    len(best['covered']), len(best['report_domains'])):
                best = result
        except ERRORS:
            continue
    if best is not None:
        fresh_native = api(f"/actions/runs/{best['run']}")
        if (not trusted_run(fresh_native, repository, workflow_data['id'], plan['pr'])
                or fresh_native['run_attempt'] != best['attempt']
                or fresh_native['head_sha'] != best['head']):
            return None
    if ready:
        if not clearance:
            return None
        # The scan donor is independent of the native report donor. Revalidate
        # its latest attempt after all other candidates/artifacts were read.
        fresh_scan = api(f"/actions/runs/{clearance['run']}")
        if (not trusted_run(fresh_scan, repository, workflow_data['id'], plan['pr'])
                or fresh_scan.get('event') != 'pull_request'
                or fresh_scan['run_attempt'] != clearance['attempt']
                or fresh_scan['head_sha'] != identity['head']):
            return None
        if not eligible_scan_donor(fresh_scan, source_job(api, fresh_scan)):
            return None
        fresh_check = find_check(api, identity, plan['pr'], clearance['scan_started_at'],
                                 clearance['check_id'])
        if fresh_check['completed_at'] != clearance['check_completed_at']:
            return None
        if not required and not needed_reports:
            return dict(**clearance, covered=set(), reports={}, report_domains=set())
        if best is None or best['covered'] != required or best['report_domains'] != needed_reports:
            return None
        best['check_id'] = clearance['check_id']
    return best


def outputs(values):
    with Path(os.environ['GITHUB_OUTPUT']).open('a') as stream:
        for key, value in values.items():
            stream.write(f'{key}={str(value).lower() if isinstance(value, bool) else value}\n')


def summary(message):
    with Path(os.environ['GITHUB_STEP_SUMMARY']).open('a') as stream:
        stream.write('\n### Pre-review validation\n\n' + message + '\n')


def current_context(plan):
    checkout = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True, timeout=60).strip()
    tree = subprocess.check_output(['git', 'rev-parse', 'HEAD^{tree}'], text=True, timeout=60).strip()
    if checkout != plan['checkout'] or tree != plan['tree']:
        raise ValueError('current checkout differs from plan')
    variables = {key: os.environ.get(key, '') for key in VARIABLES}
    return Path(WORKFLOW).read_bytes(), Path('sonar-project.properties').read_bytes(), variables


def record_validation(plan, workflow, variables, inputs):
    e = os.environ
    domains = {d: e['EXECUTED_' + d.upper()] == 'true' for d in DOMAINS}
    if any(e['EXECUTED_' + d.upper()] not in ('true', 'false') for d in DOMAINS):
        raise ValueError('invalid executed-domain selection')
    if type(plan.get('build_image')) is not bool:
        raise ValueError('invalid image-build selection')
    proof = dict(schema=1, repository=e['GITHUB_REPOSITORY'], run=int(e['GITHUB_RUN_ID']),
                 attempt=int(e['GITHUB_RUN_ATTEMPT']), checkout=plan['checkout'], tree=plan['tree'],
                 build_image=plan['build_image'],
                 head=plan.get('head', plan['checkout']), base=plan.get('base', ''),
                 pr=plan.get('pr'), domains=domains, input_digest=inputs,
                 workflow_digest=digest(workflow), variables_digest=digest(canonical(variables)))
    Path(e['SONAR_VALIDATION_EVIDENCE_PATH']).write_bytes(canonical(proof))


def current_pr(api, plan, repository):
    prefix = '/repos/' + repository
    def relative(path):
        if not path.startswith(prefix + '/'):
            raise ValueError('unexpected current-identity API path')
        return api(path[len(prefix):])
    return verify_current_pr(relative, plan, repository)


def record_clearance(plan, workflow, properties, api=request, sleeper=time.sleep):
    e = os.environ
    if e.get('SONAR_SCAN_SUCCEEDED') != 'true':
        raise ValueError('scanner quality-gate completion is required')
    identity = current_pr(api, plan, e['GITHUB_REPOSITORY'])
    # The source checkout and explicit scanner revision must equal the cleared head.
    head = subprocess.check_output(['git', '-C', 'sonar-source', 'rev-parse', 'HEAD'],
                                   text=True, timeout=60).strip()
    tree = subprocess.check_output(['git', '-C', 'sonar-source', 'rev-parse', 'HEAD^{tree}'],
                                   text=True, timeout=60).strip()
    if head != identity['head'] or tree != identity['tree']:
        raise ValueError('analyzed checkout differs from current merge-result tree')
    task = dict(line.split('=', 1) for line in
                Path('sonar-source/.scannerwork/report-task.txt').read_text().splitlines() if '=' in line)
    if task.get('serverUrl', '').rstrip('/') != 'https://sonarcloud.io' or not re.fullmatch(
            r'[A-Za-z0-9_-]+', task.get('ceTaskId', '')):
        raise ValueError('missing expected Sonar analysis task')
    started = e['SONAR_SCAN_STARTED_AT']
    timestamp(started)
    check = None
    for attempt in range(7):
        try:
            check = find_check(api, identity, plan['pr'], started)
            break
        except ERRORS:
            if attempt < 6:
                sleeper(5)
    if check is None:
        raise ValueError('native Sonar check has not confirmed this scan')
    # Re-fetch after polling; a changed target/base/head cannot inherit clearance.
    identity = current_pr(api, plan, e['GITHUB_REPOSITORY'])
    proof = dict(schema=1, repository=e['GITHUB_REPOSITORY'], run=int(e['GITHUB_RUN_ID']),
                 attempt=int(e['GITHUB_RUN_ATTEMPT']), pr=plan['pr'], **identity,
                 analyzed_revision=head, workflow_digest=digest(workflow), config_digest=digest(properties),
                 ce_task_id=task['ceTaskId'], quality_gate='OK', scan_started_at=started,
                 sonar_check_id=check['id'], check_completed_at=check['completed_at'])
    Path(e['SONAR_CLEARANCE_PATH']).write_bytes(canonical(proof))
    outputs({'sonar_check_id': check['id']})


def main():
    e = os.environ
    mode = sys.argv[1] if len(sys.argv) > 1 else 'reuse'
    if mode == 'reuse' and e['GITHUB_EVENT_NAME'] != 'pull_request':
        # Preserve the established exact-master test reuse policy for publication
        # events, including its conservative API-error fallback. No PR clearance
        # is inferred from those native validation results.
        reuse_master()
        outputs(dict(pr_reuse_validated=False, reuse_input_digest='', current_input_digest='',
                     reuse_tree='', sonar_clearance_reused=False, sonar_check_id=''))
        return
    plan = json.loads(Path(e['VALIDATION_PLAN_PATH']).read_text())
    workflow, properties, variables = current_context(plan)
    inputs = local_inputs(plan['checkout'], variables)
    if mode == 'record-validation':
        record_validation(plan, workflow, variables, inputs)
        return
    if mode == 'record-clearance':
        record_clearance(plan, workflow, properties)
        return
    if mode != 'reuse' or e['GITHUB_EVENT_NAME'] != 'pull_request':
        raise ValueError('invalid helper event/mode')
    event = json.loads(Path(e['GITHUB_EVENT_PATH']).read_text())
    if event.get('number') != plan['pr'] or event.get('action') != plan['action']:
        raise ValueError('event identity mismatch')
    identity = current_pr(request, plan, e['GITHUB_REPOSITORY'])
    ready = event['action'] == 'ready_for_review'
    required = {d for d in DOMAINS if e['REQUIRED_' + d.upper()] == 'true'}
    if any(e['REQUIRED_' + d.upper()] not in ('true', 'false') for d in DOMAINS):
        raise ValueError('invalid required-domain selection')
    source = None
    try:
        source = find_source(request, download,
                             plan, identity, e['GITHUB_REPOSITORY'], e['GITHUB_RUN_ID'], workflow,
                             properties, variables, required, inputs, ready)
    except ERRORS:
        pass  # Never print API errors or input variables.
    if ready and source is not None:
        current_pr(request, plan, e['GITHUB_REPOSITORY'])
    if ready and source is None:
        raise ValueError('readiness requires completed exact-tree Sonar clearance; validate while draft first')
    proven = source['covered'] if source else set()
    if source:
        for path, raw in source['reports'].items():
            destination = Path(path)
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(raw)
    values = dict(execute_tests=bool(required - proven), reuse_run='', reuse_attempt='', reuse_revision='',
                  reuse_workflow_digest='', pr_reuse_validated=bool(source and proven),
                  reuse_input_digest=inputs if source and proven else '', current_input_digest=inputs,
                  reuse_tree=source['tree'] if source and proven else '',
                  sonar_clearance_reused=ready, sonar_check_id=source['check_id'] if ready else '')
    for domain in DOMAINS:
        values['execute_' + domain] = domain in required - proven
        values['reused_' + domain] = domain in proven
    if source and proven:
        values.update(reuse_run=source['run'], reuse_attempt=source['attempt'],
                      reuse_revision=source['revision'], reuse_workflow_digest=digest(workflow))
    outputs(values)
    summary(('Exact current-tree Sonar clearance verified before readiness.' if ready else
             'This event requires a new Sonar scan; prior clearance is not reused.') +
            '\nNative checks reused: ' + (', '.join(sorted(proven)) or 'none') +
            '; execute: ' + (', '.join(sorted(required - proven)) or 'none') + '.')


if __name__ == '__main__':
    main()
