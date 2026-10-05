#!/usr/bin/env python3
"""Reuse independently verified test domains from one exact-SHA master push.

No artifacts or PR results are trusted. API errors and incomplete evidence leave
the affected obligations selected for normal execution. Builds are never reused:
release metadata, hidden maps and native symbols belong to the image build.
"""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.parse
import urllib.request

WORKFLOW = '.github/workflows/docker-publish.yml'
DOMAINS = {
    'backend': ('Generate backend and login locales', 'Go unit and integration tests'),
    'frontend': ('Install frozen frontend dependencies and generate operations', 'Frontend tests and type check'),
    'python': ('Python regression tests',),
}


def trusted_run(run, repository, sha, workflow_id):
    """A master push executes the workflow from its immutable head_sha."""
    return (run.get('event') == 'push' and run.get('head_branch') == 'master'
            and run.get('head_sha') == sha and run.get('path') == WORKFLOW
            and run.get('workflow_id') == workflow_id
            and run.get('repository', {}).get('full_name') == repository
            and run.get('head_repository', {}).get('full_name') == repository
            and run.get('status') == 'completed' and run.get('conclusion') == 'success'
            and type(run.get('id')) is int and run['id'] > 0
            and type(run.get('run_attempt')) is int and run['run_attempt'] > 0)


def coverage(run, jobs, repository, sha, workflow_id):
    if not trusted_run(run, repository, sha, workflow_id):
        return set()
    matches = [job for job in jobs if job.get('name') == 'ci-required']
    if len(matches) != 1:
        return set()
    job = matches[0]
    if not (job.get('head_sha') == sha and job.get('run_id') == run['id']
            and job.get('run_attempt') == run['run_attempt']
            and job.get('status') == 'completed' and job.get('conclusion') == 'success'):
        return set()
    steps = job.get('steps', [])
    for name in ('Publication policy and packaging tests', 'Classify changed inputs and deliberate publication',
                 'Verify exact-master validation evidence', 'Verify every selected obligation'):
        matches = [step for step in steps if step.get('name') == name]
        if len(matches) != 1 or matches[0].get('status') != 'completed' or matches[0].get('conclusion') != 'success':
            return set()
    return {domain for domain, names in DOMAINS.items() if all(
        len(matches := [step for step in steps if step.get('name') == name]) == 1
        and matches[0].get('status') == 'completed' and matches[0].get('conclusion') == 'success'
        for name in names)}


def find_source(api, required, repository, sha, current_run, workflow_sha, workflow_bytes):
    # Dispatch may use a different workflow revision. That cannot authorize reuse.
    if workflow_sha != sha or not re.fullmatch(r'[0-9a-f]{40}', sha):
        return None, set()
    workflow = api('/actions/workflows/docker-publish.yml')
    content = api('/contents/' + WORKFLOW + '?ref=' + sha)
    if (workflow.get('path') != WORKFLOW or content.get('encoding') != 'base64'
            or base64.b64decode(content['content']) != workflow_bytes):
        return None, set()
    runs = api('/actions/workflows/docker-publish.yml/runs?' + urllib.parse.urlencode(
        {'head_sha': sha, 'event': 'push', 'branch': 'master', 'status': 'success', 'per_page': 20}))
    best, proven = None, set()
    for run in runs['workflow_runs']:
        if str(run.get('id')) == current_run:
            continue
        # Reject untrusted identity before making any source-specific request.
        if not trusted_run(run, repository, sha, workflow['id']):
            continue
        data = api(f"/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")
        if data.get('total_count') != len(data['jobs']):
            continue  # Never accept a truncated job inventory.
        fresh = api(f"/actions/runs/{run['id']}")
        if not trusted_run(fresh, repository, sha, workflow['id']) or fresh['run_attempt'] != run['run_attempt']:
            continue  # A new pending/failed rerun cannot inherit an earlier attempt.
        covered = coverage(fresh, data['jobs'], repository, sha, workflow['id']) & required
        if len(covered) > len(proven):
            best, proven = run, covered
        if proven == required:
            break
    return best, proven


def main():
    e = os.environ
    if any(e['REQUIRED_' + domain.upper()] not in ('true', 'false') for domain in DOMAINS):
        raise ValueError('invalid required domain')
    required = {domain for domain in DOMAINS if e['REQUIRED_' + domain.upper()] == 'true'}
    source, proven = None, set()
    reason = 'This event executes its selected checks normally.'
    if required and (e['GITHUB_EVENT_NAME'] == 'workflow_dispatch' or (
            e['GITHUB_EVENT_NAME'] == 'push' and e['GITHUB_REF_TYPE'] == 'tag')):
        repository = e['GITHUB_REPOSITORY']
        if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
            raise ValueError('invalid repository')

        def api(path):
            request = urllib.request.Request('https://api.github.com/repos/' + repository + path,
                headers={'Authorization': 'Bearer ' + e['GH_TOKEN'],
                         'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'})
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response)

        try:
            source, proven = find_source(api, required, repository, e['GITHUB_SHA'],
                e['GITHUB_RUN_ID'], e['GITHUB_WORKFLOW_SHA'], Path(WORKFLOW).read_bytes())
            reason = 'No complete trusted source evidence for these domains; execute normally.'
        except (OSError, ValueError, KeyError, TypeError, AttributeError):
            # Never print API errors: they may contain credentials or response data.
            source, proven = None, set()
            reason = 'Validation evidence unavailable or malformed; execute normally.'
    outputs = {'execute_tests': bool(required - proven), 'reuse_run': '', 'reuse_attempt': '',
               'reuse_revision': '', 'reuse_workflow_digest': ''}
    for domain in DOMAINS:
        outputs['execute_' + domain] = domain in required - proven
        outputs['reused_' + domain] = domain in proven
    if proven:
        outputs.update(reuse_run=str(source['id']), reuse_attempt=str(source['run_attempt']),
            reuse_revision=e['GITHUB_SHA'],
            reuse_workflow_digest=hashlib.sha256(Path(WORKFLOW).read_bytes()).hexdigest())
        reason = (f"Reused {', '.join(sorted(proven))} from "
                  f"https://github.com/{e['GITHUB_REPOSITORY']}/actions/runs/{source['id']}/attempts/{source['run_attempt']}\n"
                  f"Exact commit/workflow revision: `{e['GITHUB_SHA']}`; workflow SHA256: "
                  f"`{outputs['reuse_workflow_digest']}`.\n"
                  f"Execute remaining domains: {', '.join(sorted(required - proven)) or 'none'}. "
                  'Release compilation, packaging and image smoke checks still execute.')
    with Path(e['GITHUB_OUTPUT']).open('a') as output:
        for key, value in outputs.items():
            output.write(f'{key}={str(value).lower() if isinstance(value, bool) else value}\n')
    with Path(e['GITHUB_STEP_SUMMARY']).open('a') as summary:
        summary.write('\n### Validation reuse\n\n' + reason + '\n')


if __name__ == '__main__':
    main()
