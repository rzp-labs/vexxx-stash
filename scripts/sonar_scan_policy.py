"""Offline scanner selection; no network, credentials, or image obligations."""
import os
import json
from datetime import datetime, timezone
from pathlib import Path
import re
import shutil
import subprocess
import time


def parse_properties(text):
    """Read this repository's key=value properties, including continued values."""
    result, pending = {}, ''
    for line in text.splitlines():
        if not pending and (not line.strip() or line.lstrip().startswith(('#', '!'))):
            continue
        line = pending + line.lstrip()
        if line.endswith('\\'):
            pending = line[:-1]
            continue
        pending = ''
        key, value = line.split('=', 1)
        if key in result:
            raise ValueError('duplicate scanner property')
        result[key] = value
    if pending:
        raise ValueError('unfinished scanner property continuation')
    return result


def properties(root):
    return parse_properties((root / 'sonar-project.properties').read_text())


def matches(path, pattern):
    # Sonar path patterns: ** spans directories, * and ? stay within one.
    regex = ''
    i = 0
    while i < len(pattern):
        if pattern[i:i + 3] == '**/':
            regex += '(?:.*/)?'
            i += 3
        elif pattern[i:i + 2] == '**':
            regex += '.*'
            i += 2
        elif pattern[i] == '*':
            regex += '[^/]*'
            i += 1
        elif pattern[i] == '?':
            regex += '[^/]'
            i += 1
        else:
            regex += re.escape(pattern[i])
            i += 1
    return bool(re.fullmatch(regex, path))


def analyzed_input(path, config):
    # Documentation-only changes do not replace the master coverage snapshot.
    if path.endswith('.md') or path.startswith('docs/'):
        return False
    # The project's new scope scans the whole repository. Keep configured
    # exclusions, but do not reconstruct the removed legacy source/test roots.
    return not any(matches(path, pattern)
                   for pattern in config.get('sonar.exclusions', '').split(','))


def scanner_plan(name, kind, ref, repository, event, paths, groups, config,
                 enabled=False):
    groups = dict(groups)
    result = {'sonar_scan': False, 'sonar_status': 'disabled',
              'sonar_added_domains': '', 'sonar_go_paths': '',
              'sonar_lcov_paths': '', 'sonar_coverage_backend': False,
              'sonar_coverage_frontend': False}
    if not enabled:
        return groups, result
    if event.get('deleted'):
        result['sonar_status'] = 'deleted'
        return groups, result
    if name == 'pull_request':
        pr = event.get('pull_request', {})
        if (pr.get('head', {}).get('repo', {}).get('full_name') != repository
                or pr.get('base', {}).get('repo', {}).get('full_name') != repository):
            result['sonar_status'] = 'fork_or_missing_trust_metadata'
            return groups, result
        if not pr.get('base', {}).get('ref'):
            result['sonar_status'] = 'missing_target_branch'
            return groups, result
        # PR quality gates use new-code metrics; keep affected-domain validation.
    elif name == 'push' and kind == 'branch' and ref == 'master':
        if not any(analyzed_input(p, config) for p in paths):
            result['sonar_status'] = 'analyzed_inputs_unchanged'
            return groups, result
        # A full master snapshot cannot carry forward omitted coverage reports.
        added = [d for d in ('backend', 'frontend') if not groups[d]]
        result['sonar_added_domains'] = ','.join(added)
        for domain in added:
            result['sonar_coverage_' + domain] = True
    else:
        result['sonar_status'] = 'event_outside_scanner_scope'
        return groups, result
    result['sonar_scan'] = True
    selected = [d for d in ('backend', 'frontend')
                if groups[d] or result['sonar_coverage_' + d]]
    result['sonar_status'] = 'import_' + ('_and_'.join(selected) or 'no_coverage')
    if groups['backend'] or result['sonar_coverage_backend']:
        result['sonar_go_paths'] = config['sonar.go.coverage.reportPaths']
    if groups['frontend'] or result['sonar_coverage_frontend']:
        result['sonar_lcov_paths'] = config['sonar.javascript.lcov.reportPaths']
    return groups, result


def verify_source_snapshot(root, tested_sha, analysis_sha, config):
    if any(not re.fullmatch(r'[0-9a-f]{40}', sha)
           for sha in (tested_sha, analysis_sha)):
        raise ValueError('invalid source revision')
    # Bind the whole tracked source tree, including files outside scanner scope.
    # Sonar's supported PR HEAD must be byte-identical to the merge result.
    trees = [subprocess.check_output(['git', 'rev-parse', sha + '^{tree}'],
                                    cwd=root, text=True).strip()
             for sha in (tested_sha, analysis_sha)]
    if trees[0] != trees[1]:
        raise ValueError('tested merge and analysis HEAD trees differ; update the branch before scanning')


def verify_current_pr(api, plan, repository, sleeper=time.sleep):
    """Bind current GitHub PR metadata and both complete Git trees to the plan."""
    number = plan.get('pr')
    if isinstance(number, bool) or not re.fullmatch(r'[1-9][0-9]*', str(number)):
        raise ValueError('invalid PR number')
    prefix = '/repos/' + repository
    # GitHub computes mergeability asynchronously after PR updates. Retry only
    # that pending state; reject changed identities and settled conflicts now.
    for attempt in range(6):
        pr = api(prefix + '/pulls/' + str(number))
        for side in ('head', 'base'):
            if pr.get(side, {}).get('repo', {}).get('full_name') != repository:
                raise ValueError('PR repository changed')
            if pr[side].get('sha') != plan.get(side):
                raise ValueError('PR ' + side + ' changed; run validation again')
        if pr.get('state') != 'open' or pr['base'].get('ref') != plan.get('target'):
            raise ValueError('PR target or state changed')
        if pr.get('mergeable') is not None or attempt == 5:
            break
        sleeper(2)
    merge = pr.get('merge_commit_sha', '')
    if pr.get('mergeable') is not True or not re.fullmatch(r'[0-9a-f]{40}', merge):
        raise ValueError('current PR merge result is unavailable')
    merged = api(prefix + '/git/commits/' + merge)
    head = api(prefix + '/git/commits/' + plan['head'])
    if [p.get('sha') for p in merged.get('parents', [])] != [plan['base'], plan['head']]:
        raise ValueError('current PR merge parents changed')
    tree = plan.get('tree', '')
    if (not re.fullmatch(r'[0-9a-f]{40}', tree)
            or merged.get('tree', {}).get('sha') != tree
            or head.get('tree', {}).get('sha') != tree):
        raise ValueError('current PR HEAD and merge trees differ; update the branch')
    return {'head': plan['head'], 'base': plan['base'], 'target': plan['target'],
            'merge': merge, 'tree': tree}


def prepare_source_reports(root, source_root, tested_sha, analysis_sha,
                           go_paths, lcov_paths):
    config = properties(root)
    if not re.fullmatch(r'[0-9a-f]{40}', analysis_sha):
        raise ValueError('invalid analysis revision')
    checked_out = subprocess.check_output(['git', 'rev-parse', 'HEAD'],
                                          cwd=source_root, text=True).strip()
    if checked_out != analysis_sha:
        raise ValueError('scanner checkout does not match the analysis revision')
    allowed = set(config['sonar.go.coverage.reportPaths'].split(',')) | {
        config['sonar.javascript.lcov.reportPaths']}
    reports = list(filter(None, (go_paths + ',' + lcov_paths).split(',')))
    verify_source_snapshot(root, tested_sha, analysis_sha, config)
    for report in reports:
        if report not in allowed:
            raise ValueError('unexpected coverage report path')
        source = root / report
        if not source.is_file() or not source.stat().st_size:
            raise ValueError('selected domain coverage report missing or empty')
        destination = source_root / report
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
    if reports:
        # No extra install/generation. Only reuse artifacts after input equality.
        dependencies = root / 'ui/v2.5/node_modules'
        destination = source_root / 'ui/v2.5/node_modules'
        if dependencies.is_dir() and not destination.exists():
            destination.symlink_to(dependencies.resolve(), target_is_directory=True)
        generated = root / 'ui/v2.5/src/core/generated-graphql.ts'
        if generated.is_file():
            destination = source_root / 'ui/v2.5/src/core/generated-graphql.ts'
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(generated, destination)


if __name__ == '__main__':
    root = Path.cwd()
    if os.environ.get('GITHUB_EVENT_NAME') == 'pull_request':
        from reuse_pr_packaging import request
        plan = json.loads(Path(os.environ['VALIDATION_PLAN_PATH']).read_text())
        repository = os.environ['GITHUB_REPOSITORY']
        prefix = '/repos/' + repository
        verify_current_pr(lambda path: request(path[len(prefix):]), plan, repository)
    prepare_source_reports(root, root / 'sonar-source', os.environ['TESTED_SHA'],
                           os.environ['ANALYSIS_SHA'], os.environ['GO_REPORTS'],
                           os.environ['LCOV_REPORTS'])
    with Path(os.environ['GITHUB_OUTPUT']).open('a') as output:
        output.write('scan_started_at=' + datetime.now(timezone.utc).isoformat() + '\n')
