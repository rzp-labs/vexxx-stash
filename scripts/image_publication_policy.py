#!/usr/bin/env python3
"""Plan validation and deliberate publication without network or credential access."""
import json
import os
from pathlib import Path
import re
import subprocess

RELEASE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
SHA = re.compile(r"[0-9a-f]{40}")
LABEL = re.compile(r"[a-z0-9]+(?:-[a-z0-9]+)*")


def affected(paths):
    """Ignore documentation/tools; conservatively validate runtime/build inputs."""
    tests = image = False
    for path in paths:
        if path in ("ui/v2.5/README.md", "ui/v2.5/src/locales/README.md",
                    "pkg/sqlite/migrations/README.md", "pkg/plugin/examples/README.md",
                    "pkg/plugin/examples/react-component/README.md"):
            continue
        if path in (".github/workflows/docker-publish.yml", ".dockerignore", "Makefile",
                    "go.mod", "go.sum", "gqlgen.yml", ".gqlgenc.yml",
                    "scripts/check_intel_runtime.py", "scripts/generateLoginLocales.go",
                    "scripts/getDate.go"):
            tests = image = True
        elif path.startswith(("cmd/gpu-generation-lab/", "scripts/gpu_generation/")):
            tests = True
        elif path == "scripts/test_check_intel_runtime.py":
            tests = True
        elif path.startswith(("cmd/", "internal/", "pkg/", "graphql/", "ui/")) or (
                "/" not in path and path.endswith(".go")):
            tests = True
            image |= not (path.endswith("_test.go") or (
                path.startswith("ui/") and re.search(r"\.(?:test|spec)\.[cm]?[jt]sx?$", path)))
        elif path.startswith("docker/build/x86_64/"):
            tests = image = True
    return tests, image


def plan(event_name, ref_type, ref_name, sha, repository, event, paths=(), run_id="1", attempt="1"):
    if not SHA.fullmatch(sha) or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid source identity")
    local = "vexxx-ci:" + sha
    result = {"run_tests": False, "build_image": False, "publish": False,
              "local_ref": local, "image_ref": "", "sha_ref": "", "version": "ci-" + sha[:12]}
    if event.get("deleted"):
        return result
    if event_name == "push" and ref_type == "tag":
        # The workflow glob is only a prefilter. This is the exact version gate.
        if not RELEASE.fullmatch(ref_name) or len(ref_name) > 128:
            raise ValueError("release must be exact vMAJOR.MINOR.PATCH without leading zeros or suffixes")
        image = "ghcr.io/" + repository.lower()
        result.update(run_tests=True, build_image=True, publish=True,
                      image_ref=image + ":" + ref_name, sha_ref=image + ":sha-" + sha, version=ref_name)
    elif event_name == "workflow_dispatch":
        result.update(run_tests=True, build_image=True)
        inputs = event.get("inputs") or {}
        publish = inputs.get("publish_test_image", "false")
        if publish not in (True, False, "true", "false"):
            raise ValueError("publish_test_image must be boolean")
        if publish is True or publish == "true":
            label = inputs.get("test_label", "")
            if not isinstance(label, str) or not 1 <= len(label) <= 32 or not LABEL.fullmatch(label):
                raise ValueError("test_label must be 1..32 lowercase letters/digits with single internal hyphens")
            if not re.fullmatch(r"[1-9][0-9]{0,19}", run_id) or not re.fullmatch(r"[1-9][0-9]{0,5}", attempt):
                raise ValueError("invalid workflow run identity")
            tag = f"test-{label}-{run_id}-{attempt}"
            result.update(publish=True, image_ref="ghcr.io/" + repository.lower() + "-test:" + tag,
                          version=tag)
    elif event_name == "pull_request" or (event_name == "push" and ref_type == "branch" and ref_name == "master"):
        tests, image = affected(paths)
        result.update(run_tests=tests, build_image=image)
    else:
        raise ValueError("unsupported publication event")
    return result


def changed_paths(event_name, event, cwd):
    if event_name == "pull_request":
        pr = event["pull_request"]
        base, head = pr["base"]["sha"], pr["head"]["sha"]
    elif event_name == "push":
        base, head = event["before"], event["after"]
    else:
        return []
    if not SHA.fullmatch(base) or not SHA.fullmatch(head):
        raise ValueError("invalid diff identity")
    if base == "0" * 40:
        command = ["git", "ls-tree", "-r", "--name-only", "-z", head]
    else:
        if event_name == "pull_request":
            base = subprocess.check_output(["git", "merge-base", base, head], cwd=cwd, timeout=60).decode().strip()
            if not SHA.fullmatch(base):
                raise ValueError("invalid merge base")
        # No rename folding: both deleted and added paths influence classification.
        command = ["git", "diff", "--no-renames", "--name-only", "-z", base, head]
    return [os.fsdecode(p) for p in subprocess.check_output(command, cwd=cwd, timeout=60).split(b"\0") if p]


def main():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    name, kind = os.environ["GITHUB_EVENT_NAME"], os.environ["GITHUB_REF_TYPE"]
    paths = changed_paths(name, event, Path.cwd()) if (
        name == "pull_request" or (name == "push" and kind == "branch" and not event.get("deleted"))) else []
    result = plan(name, kind, os.environ["GITHUB_REF_NAME"], os.environ["GITHUB_SHA"],
                  os.environ["GITHUB_REPOSITORY"], event, paths,
                  os.environ.get("GITHUB_RUN_ID", "1"), os.environ.get("GITHUB_RUN_ATTEMPT", "1"))
    print(json.dumps(result, sort_keys=True))
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        for key, value in result.items():
            output.write(f"{key}={str(value).lower() if isinstance(value, bool) else value}\n")


if __name__ == "__main__":
    main()
