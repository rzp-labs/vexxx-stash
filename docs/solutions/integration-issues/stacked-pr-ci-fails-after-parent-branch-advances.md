---
title: Stacked PR CI fails after its parent branch advances
date: 2026-10-13
category: integration-issues
module: CI image publication policy
problem_type: integration_issue
component: infrastructure
symptoms:
  - "ci-required fails in scripts/image_publication_policy.py with ValueError: checkout is not the expected PR base/head merge"
  - "gh run rerun --failed reproduces the same failure"
  - Sibling PRs on the same parent stayed green because their runs started before the parent moved
root_cause: wrong_api
resolution_type: workflow_improvement
severity: medium
tags: [stacked-prs, github-actions, pull-request-base-sha, merge-ref, ci-required, squash-merge]
---

# Stacked PR CI fails after its parent branch advances

> Historical note: the policy scripts described here (`scripts/image_publication_policy.py`,
> `scripts/sonar_scan_policy.py`, the reuse helpers) and the `docker-publish.yml`
> workflow were removed by VEX-146. The replacement `ci.yml` trusts GitHub's
> merge protections instead of validating checkout identity, so the failure mode
> below no longer applies. This document is kept for history.

## Problem
A PR based on another PR's branch (not `master`) failed `ci-required` after a new commit was pushed to that parent branch. Re-running CI didn't help. In this session the failure only cleared after the parent PR merged and the child was rebased onto `master` and pushed.

## Symptoms
- `ci-required` fails in `scripts/image_publication_policy.py` with `ValueError: checkout is not the expected PR base/head merge`.
- `gh run rerun <id> --failed` fails the same way.
- Sibling child PRs on the same parent stayed green, but only because their runs started before the parent moved. A new run on a sibling would hit the same check if its payload base was still stale.

## What Didn't Work
All three observations come from this session, on #77.
- **Re-running the failed job.** It failed the same way. The rerun appeared to reuse the original event payload and its stale `base.sha`.
- **Starting a new run with a fresh event.** The workflow listens for `edited` (`.github/workflows/docker-publish.yml:7`). The `gh pr edit` in this session produced an `edited` run, and it failed the same way.
- **Changing the child PR.** The child was test-only, so the real fix (a change to the policy script) didn't belong in it.

## Solution
This is a change to how the stack is merged and rebased. The policy script and the Actions workflow are unchanged.

1. Merge the parent PR first.
2. Confirm each child's base is now `master`, and retarget it if not. In this session GitHub retargeted #75–#78 itself after #74 was squash-merged.
3. After a squash merge, a child still carries the parent's original commits and may conflict with the squashed result. #75–#78 all showed `CONFLICTING`. Replay only each child's own commits onto `master`:

   ```bash
   git fetch origin
   # <old-parent-tip> is the parent commit the child was built on
   git rebase --onto origin/master <old-parent-tip>
   git push --force-with-lease=<branch>:<old-child-tip> origin HEAD:<branch>
   ```

4. The push triggers a `synchronize` run. After rebasing, #75, #76, #77 and #78 all passed `ci-required`.

## Why This Works
`validate_pr_merge` (`scripts/image_publication_policy.py:164-174`) requires the Actions checkout's parents to be exactly `pull_request.base.sha` and then `pull_request.head.sha`:

```python
parents = subprocess.check_output(
    ["git", "rev-list", "--parents", "-n", "1", checkout], ...).decode().split()
if parents != [checkout, base, head]:
    raise ValueError("checkout is not the expected PR base/head merge")
```

On #77, the merge commit's base parent was the parent branch's new tip. The payload's `base.sha` was still #74's branch tip from before its review-fix push, and the GitHub API reported the same stale value. As long as the payload keeps the old base and the checkout uses the advanced one, this comparison fails.

Rebasing the child and pushing it gives a new head and a new run. In this session, that run's base matched the checkout. The run must also pass `verify_source_snapshot` (`scripts/image_publication_policy.py:175-176`), which requires the merge tree and the head tree to match.

`master` requires a branch to be up to date with `master` before it can merge. That doesn't stop an earlier CI run from hitting the same stale-base mismatch. A `master`-based PR whose run starts after `master` advances might fail the same way if its payload base is stale. That is an inference; it wasn't observed here.

## Prevention
- In a stack, a push to the parent branch can make children fail validation. If the parent has to change (for example, to address review feedback), expect children with outstanding or future runs to fail until each child is rebased onto the new base or the parent merges.
- Merge stacks from the bottom up, and expect the rebase in step 3 after a squash merge.
- A policy fix is possible but would take more than one change. Checking the merge commit's base parent against the live base branch tip would remove the stale-payload problem from that one comparison. Other checks still use the payload base:
  - changed-path analysis (`scripts/image_publication_policy.py:128-129`)
  - the exported base and merge base (`:202-205`)
  - Sonar's PR identity check (`scripts/sonar_scan_policy.py:143-146`)

  These are security-relevant identity checks, so change them together and on purpose. `test_image_publication_policy.py` covers wrong-base, wrong-head and non-merge checkouts (`test_pr_merge_rejects_wrong_head_base_or_nonmerge_checkout`), but has no case for a base that has advanced.

## Related Issues
- Related to VEX-65 (supported stacked PR base branches) and VEX-108 (CI for stacked PRs with arbitrary base branches). VEX-108 is marked done; this failure happened afterwards.
- PRs #74–#78 (Sonar coverage series), where this was diagnosed.
