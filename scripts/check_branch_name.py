#!/usr/bin/env python3
"""Validate the pull_request source branch naming convention from the event payload.

Reads GITHUB_EVENT_PATH and GITHUB_REPOSITORY; fails with ValueError when the
head branch violates the convention (username/VEX-123), allowing the exact
legacy exceptions, same-repository release-vX.Y.Z branches, and verified
dependabot[bot] branches.
"""
import json
import os
import re
import sys
from pathlib import Path

RELEASE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
ISSUE_BRANCH = re.compile(r"[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*/[Vv][Ee][Xx]-[1-9][0-9]*(?:-[A-Za-z0-9]+)*")
NAMING_REPOSITORY = "rzp-labs/vexxx-stash"
LEGACY_BRANCHES = frozenset(("master", "codex/ct102-media-generation", "feat/posthog-instrumentation"))


def validate_pr_source_branch(repository, pr):
    """Validate source metadata; only repository-owned heads receive exceptions."""
    if repository != NAMING_REPOSITORY:
        return
    head = pr.get("head") if isinstance(pr, dict) else None
    branch = head.get("ref") if isinstance(head, dict) else None
    head_repo = head.get("repo") if isinstance(head, dict) else None
    full_name = head_repo.get("full_name") if isinstance(head_repo, dict) else None
    if not isinstance(branch, str) or not branch or not isinstance(full_name, str) or not re.fullmatch(
            r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", full_name):
        raise ValueError("missing or malformed PR source branch/repository metadata")
    if ISSUE_BRANCH.fullmatch(branch):
        return
    if full_name == repository:
        if branch in LEGACY_BRANCHES or branch.startswith("release-") and RELEASE.fullmatch(branch[8:]):
            return
        author = pr.get("user")
        if (branch.startswith("dependabot/") and len(branch) > len("dependabot/")
                and isinstance(author, dict) and author.get("login") == "dependabot[bot]"
                and author.get("type") == "Bot"):
            return
    raise ValueError("PR source branch must use username/VEX-123 with an optional hyphenated suffix; "
                     "only same-repository master, exact legacy branches, release-vX.Y.Z, "
                     "and verified Dependabot branches are exempt")


def main():
    repository = os.environ["GITHUB_REPOSITORY"]
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    pr = event.get("pull_request")
    if not isinstance(pr, dict):
        raise ValueError("check_branch_name requires a pull_request event payload")
    validate_pr_source_branch(repository, pr)


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        print(f"::error::{error}", file=sys.stderr)
        sys.exit(1)
