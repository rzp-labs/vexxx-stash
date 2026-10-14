---
title: CI trusts branch protection and the merge queue instead of proving earlier runs
date: 2026-10-14
category: architecture-patterns
module: CI and release workflows
problem_type: architecture_pattern
component: infrastructure
severity: high
applies_when:
  - Changing .github/workflows/ci.yml or release.yml
  - Tempted to skip work by reusing results from an earlier workflow run
  - A release or landing run repeats tests that already passed
  - Adding a new required check or gate to master
tags: [ci, github-actions, merge-queue, release, evidence-reuse, ci-required, ghcr]
---

# CI trusts branch protection and the merge queue instead of proving earlier runs

## Context
Until #82–#84, CI was one serial job in `.github/workflows/docker-publish.yml` (669 lines, removed in #84) plus about 4,400 lines of Python, also removed in #84. Most of that Python proved that an earlier run had already validated the same tree, so a later run could skip work: `reuse_master_validation.py` (master → tag), `reuse_pr_packaging.py` (PR → master), `reuse_sonar_validation.py`, and the planner/policy in `image_publication_policy.py` and `sonar_scan_policy.py`. The proofs compared artifact digests, tree fingerprints and workflow digests, and verified a signed OIDC receipt with a hand-built RSA key encoder.

Missing reusable evidence normally re-ran only the affected domains, and missing exact-tree Sonar clearance on `ready_for_review` failed the run outright. Even so, the design cost more than it saved:

- **The proofs were a small saving.** Deleting the three reuse scripts and their tests removed about 2,700 lines (about 3,400 counting the Sonar scan policy) but only about 3% of CI minutes (session history: 2026-10-13 audit). The real minutes were the production frontend compile (35%) and Go tests (27%).
- **The proofs caused failures.** `validate_pr_merge` required the checkout's parents to equal the event payload's `base.sha`. When a base branch advanced, that payload went stale and `ci-required` failed with `checkout is not the expected PR base/head merge`. It first hit stacked PRs #75–#78 after their parent #74 took a review-fix push. Re-running the job or triggering an `edited` event reused the stale payload and failed the same way; only a new push whose head contained the new base cleared it. On 2026-10-14, #64 and #66 hit the same failure after master advanced, after their real test fixes had already been pushed (runs 38506675416 and 38507296783).
- **Reuse raced the thing it reused.** The v0.3.0 tag was pushed 43 seconds after the master run for the same commit started. The release run found no master evidence yet, so it re-ran Go and frontend tests (about 3.5 min) and took 23 minutes in total.
- **The layout blocked its own fix.** Reuse proofs located the `ci-required` job and matched exact step names, so splitting the job into parallel jobs would have broken every reuse path.
- **Landings ran a second validation.** PRs tested the synthetic merge, then master re-ran the same tree (8.6 minutes for the v0.3.0 landing, run 38511865455).

An earlier session (2026-10-13) had already agreed the direction in the Linear doc "CI pipeline architecture direction": per-domain jobs and a merge queue (session history). That plan still kept master → tag evidence reuse. The 2026-10-14 rebuild dropped reuse entirely.

## Guidance
**Let GitHub's guarantees carry trust. Don't rebuild them from artifacts.** Master ruleset 24450823 requires `ci-required` and runs a merge queue (squash, all-green grouping). A commit on master has therefore passed `ci-required` on the exact tree that landed. Later workflows rely on that and do not look up or verify earlier runs.

The shape that implements this:

- **`ci.yml`** runs on `pull_request`, `merge_group` and `push` to master (`.github/workflows/ci.yml:4-8`). `backend`, `frontend`, `python` and `image` are separate parallel jobs. `ci-required` is a few lines: it `needs` every job, runs `if: always()`, and fails only when a dependency failed or was cancelled (`ci.yml:259-268`). A skipped job passes.
- **PRs run only what their files need.** A bash `case` over the PR's changed files (`ci.yml:41-56`) sets each domain, and each job's `if:` reads it (`ci.yml:69`, `105`, `143`, `160`). On `merge_group` and master pushes every domain runs.
- **Standalone production compiles skip PR events.** `make flags-release flags-pie stash` and `make ui-only` run only when `github.event_name != 'pull_request'` (`ci.yml:88-95`, `128-131`), so they run in the `merge_group` gate and again in the post-merge master push, which nobody waits on.
- **The normal release trigger is a `VERSION` change landing on master**, through the merge queue like any other PR. `release.yml` runs on that push (`release.yml:6`); manual dispatch exists for retries. It builds once, smoke-tests, pushes that exact image as `vX.Y.Z`, `latest` and `sha-<commit>`, then creates the git tag (`release.yml:161-196`). It does not re-run tests.
- **Strict up-to-date is off.** The merge queue already tests each PR against current master. Keeping strict as well sends every other open PR back through "update branch" after each merge.

Keep these properties. They are not reuse machinery:

- Build once and publish the smoke-tested image without rebuilding (`release.yml:164-172`).
- Write permissions only in the publish job (`release.yml:147`). The build job has a read-only GitHub token. Real release builds receive the PostHog upload secret; PR dry runs do not.
- Never overwrite a published version. A registry lookup error fails the run rather than counting as "absent" (`release.yml:165-175`).
- The required check name `ci-required`, pinned action SHAs, and the release identity build arguments.

## Why This Matters
Reuse proofs save little for their cost: a missing piece of evidence re-runs the affected work anyway, so the effort spent proving rarely saves time, and a broken proof (the stale-base check) can fail a valid run outright. They also tie the workflow's job and step names to Python code, so restructuring CI means rewriting the proofs. Relying on the ruleset and merge queue means a skipped run is a plain policy decision anyone can read in YAML.

Observed after the rebuild: the first master-push run of `ci.yml` passed in 9.7 minutes with all domains, the image and Sonar, and nobody waits on it. PRs that touch shared paths still take about 5 minutes, about the same as before, because the backend job, including setup and generation (about 3–4 min), plus Sonar (about 1 min) is the critical path. The savings show up on single-domain PRs, on landings (no master re-run to wait for), and on releases (no tag race, no test re-run).

## When to Apply
- Before adding any step that downloads artifacts from, or queries the status of, another workflow run in order to skip work. Ask whether the ruleset or merge queue already guarantees it. If it does, skip the work by event or path instead.
- Before adding a gate. Prefer a job that `ci-required` already depends on over a new required check, and keep `ci-required` a plain result aggregator.
- When a release or landing run seems slow, look first for work repeated on the same tree. Fix it by trigger choice, not by proving the earlier run.

## Examples
Pitfalls hit while building this. The code alone doesn't make them obvious:

- **`dorny/paths-filter` has no "last match wins".** A filter list that starts with `**` matches every file, so every domain ran on docs-only PRs. The fix was an explicit first-match `case`. It also has to classify both names of a renamed file (`ci.yml:41`, `previous_filename`), and exempt only the listed documentation files: bare `*.md` would have skipped Markdown that the UI renders (`ui/v2.5/src/docs/**`).
- **A nonzero exit from `docker manifest inspect` doesn't say why.** The workflow treats only `manifest unknown` / `no such manifest` / `not found` as "absent" (`release.yml:67`, `168`); any other failure, such as auth or network, fails the run. Otherwise a transient error could let a run overwrite a published version.
- **`docker buildx imagetools inspect --format` differs for single-platform images.** `.Image` is the config itself, so the recovery check reads `{{index .Image.Config.Labels "org.opencontainers.image.revision"}}` (`release.yml:63`). Ranging over `.Image` as a platform map fails. Checked against the published `v0.3.0` image, it returns its revision and `0.3.0`.
- **Tags created with `GITHUB_TOKEN` don't trigger other workflows.** That is why `release.yml` can create `vX.Y.Z` itself without starting a second publish.
- **Moving code between files inside a PR stack trips Sonar's duplication gate.** While the stack had the branch-name validator in both `check_branch_name.py` (#82) and the old policy module (deleted in #84), SonarCloud's quality gate on #82 reported 6.5% duplication against a 3% threshold. The fix was to have the old module import the new one, so only one copy existed at each step of the stack.
- **When squash-merging a stack, rebase each child with `git rebase --onto origin/master <old-parent-tip>`, then push with `git push --force-with-lease=<branch>:<old-child-tip> origin HEAD:<branch>`.** `<old-parent-tip>` is the parent commit in the child's own history, not a later rebased copy of the parent. With branch deletion on merge, GitHub retargets the child to master by itself. The child then shows as conflicting or behind, because it still carries the parent's original, pre-squash commits (#75–#78 and #82–#84).

Not yet exercised as of 2026-10-14: a `merge_group` run, and a real publish through `release.yml`. The first queued merge and the first `release-vX.Y.Z` PR will be the first runs of those paths.
