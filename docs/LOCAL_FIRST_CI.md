# Local validation before push

Validate a coherent candidate locally before pushing it. CI independently confirms
its PR merge tree or landed master tree. Run focused tests while iterating, then
run the affected complete checks once after the inputs stop changing. Existing Go
build/module caches and the pnpm store remain useful; cached passing test results
are not acceptance evidence.

| Changed inputs | Local and CI confirmation | Automatic image |
| --- | --- | --- |
| Backend source/tests or Go dependencies | Generate backend/login locales; unit and integration tests; production backend compile | No |
| Frontend source/tests or pnpm dependencies | Frozen install; generate operations; Vitest; TypeScript | No |
| Schema, generators, workflow, unknown/shared inputs | Both domains and Python regression tests | No |
| Python scripts | Python regression suites | No |
| UI locale inputs | Both domains | No |
| Docker packaging, `.dockerignore`, Makefile, runtime packaging probe | Both domains plus build/startup smoke of Linux amd64 image | Yes |
| Explicit documentation paths | Cheap change-detection and aggregate jobs | No |

## CI pipeline

`ci.yml` runs on pull requests, merge groups, and pushes to `master`. A `changes`
job detects which inputs changed; `backend`, `frontend`, `python`, and `image`
(Docker-packaging changes only) run in parallel, plus `branch-name` on PRs and a
`sonar` scan. The aggregate `ci-required` job is the required check on `master`.
Pushing to a PR branch cancels its in-progress run. Each run executes its jobs
fresh; there is no cross-run evidence reuse, so master branch protection (and the
merge queue, once enabled) is what guarantees the tested tree is what lands.

PR runs do not build a production frontend bundle or push images. When a PR
touches Docker packaging, the `image` job builds and smoke-tests the real
`linux/amd64` image but publishes nothing.

## Existing commands

Use Python 3.11 or newer for the existing regression tests. No dependency
installation is needed for these fast offline checks:

```sh
python3 -m unittest discover -s scripts -p 'test_*.py'
```

For a candidate affecting both application domains, the existing commands are:

```sh
export POSTHOG_UPLOAD_REQUIRED=false
make pre-ui generate generate-login-locale
go test -count=1 -tags 'sqlite_stat4 sqlite_math_functions integration' ./...
(cd ui/v2.5 && pnpm test && pnpm run check)
STASH_NOLEGACY=true make ui-only
make flags-release flags-pie stash
```

For backend-only changes, use `make generate-backend generate-login-locale`, the
same tagged Go test command, and `make flags-release flags-pie stash`. The existing
`touch-ui` generation prerequisite provides the embed placeholder: this compile
checks backend linking, not a complete UI deliverable. For frontend-only changes,
use `make pre-ui generate-ui`, the frontend test/type commands, and
`STASH_NOLEGACY=true make ui-only`.
Python changes use `python3 -m unittest discover -s scripts -p 'test_*.py'`; when
present, also run the existing suite in `scripts/gpu_generation`.

`pnpm run validate` remains the documented lint/type/format acceptance command.

These commands can download modules/packages and use CPU, memory and disk for
code generation, tests and native compilation. They do not build/push an image or
upload symbols. Freeze the candidate and record its commit/tree plus any remaining
patch before push; later relevant changes require renewed evidence.

## Packaging and deliberate delivery

Use the existing `make docker-build` only after affected checks pass and packaging
has changed, or an image candidate/release is deliberately selected.
Its target is Linux amd64. The Docker frontend has an explicit 4 GiB Node heap
cap; backend generation/compilation uses four-way parallelism and a 2 GiB Go
soft memory limit. These settings apply only to build stages, not the final app. On the Mac, prefer the existing OrbStack route after
confirming its builder can build and run that target; do not infer amd64 execution
from the host architecture. The reported Determinate Nix capability is an
alternative, not a requirement to convert this project. Linux packaging/startup
can be checked locally. Actual GPU drivers/devices, mounts, permissions and
container integration still need the intended target environment when claimed.

## Fork release versions

`VERSION` is the fork's source of truth, starting at `0.1.0` because this fork
has no published Git tags or GitHub releases. Inherited upstream tags do not
identify fork releases. Use SemVer: patch for compatible fixes, minor for added
functionality, major for incompatible changes once the public API is stable.
Before 1.0, document compatibility changes in the release notes.

Released application/UI metadata, the OCI version label, PostHog `$app_version`
and UI source-map release version use `0.1.0`; Git and GHCR tags use `v0.1.0`.
The commit SHA remains separate revision/build provenance. Local and ordinary CI
builds use `0.1.0-dev+sha.<12hex>`; a feature name or transport tag is never the
application version. `make` resolves this default with Python 3 and `VERSION`.
The release build bakes the version into the backend and UI before compiling.

Releasing is a normal reviewed change: open a PR from a `release-vX.Y.Z` branch
that bumps `VERSION`, and merge it. When that merge lands on `master`,
`release.yml` builds the Linux amd64 image once, smoke-tests it, and pushes the
same image to `ghcr.io/rzp-labs/vexxx-stash` as `vX.Y.Z`, `latest` and
`sha-<commit>`, then creates the git tag `vX.Y.Z`. It does not re-run tests:
master already required `ci-required`. It refuses to overwrite an existing
version tag, so a new release needs a new `VERSION` value. A PR that edits
`release.yml` runs a dry-run build without publishing, and the workflow can be
re-run manually via `workflow_dispatch`. Ordinary PR and master validation never
publishes images.

## PostHog build and runtime configuration

Published builds embed the same `VITE_PUBLIC_POSTHOG_PROJECT_TOKEN` and
`VITE_PUBLIC_POSTHOG_HOST` into the UI and Go backend. Backend events and
lifecycle logs use that bundled destination automatically; no runtime toggle
or duplicate production configuration is required. These are public ingestion
settings, never the personal API key used to upload symbols/source maps.
A complete `POSTHOG_PROJECT_TOKEN` + `POSTHOG_HOST` runtime pair overrides the
backend destination. An incomplete runtime pair uses the bundled pair as a
whole, so project tokens and hosts are never mixed across configurations.
Development/local builds without both public build settings leave telemetry
unconfigured; they do not invent credentials or a project destination.

Vite's source-map upload condition includes
`env.POSTHOG_UPLOAD_REQUIRED !== "false"`: explicitly setting the string `false`
prevents frontend uploads even when API key/project ID are present. Leaving it
unset can enable uploads. The Docker backend uploads only when
`POSTHOG_UPLOAD_REQUIRED` equals `true`, but the Makefile's
`build-release-posthog-*` targets upload unconditionally; do not use those targets
for validation. Retain ordinary compile targets and the explicit false setting
when integrating branches. Reconcile the release build's upload build args
separately; workflow environment variables alone do not become Docker build args.

The checked-in Compose template names the same application package but defaults
`IMAGE_TAG` to `edge`, with no explicit `pull_policy`. The user's existing
production Compose is reported to use `latest`; it has not been inspected or
changed by this task. Use that existing configuration for the deliberate latest
candidate, and verify its effective pull behavior before expecting cached-image
refresh. No production deployment is performed here.
