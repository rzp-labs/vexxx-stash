# Local validation before push

Validate a coherent candidate locally before pushing it. CI independently confirms
its PR merge tree or landed master tree. Run focused tests while iterating, then
run the affected complete checks once after the inputs stop changing. Existing Go
build/module caches and the pnpm store remain useful; cached passing test results
are not acceptance evidence.

| Changed inputs | Local and CI confirmation | Automatic image |
| --- | --- | --- |
| Backend source/tests or Go dependencies | Generate backend/login locales; unit and integration tests; production backend compile | No |
| Frontend source/tests or pnpm dependencies | Frozen install; generate operations; Vitest; TypeScript; production UI compile | No |
| Schema, generators, workflow, unknown/shared inputs | Both domains and Python regression tests | No |
| Python scripts | Existing Python regression suites | No |
| UI locale inputs | Both domains | No |
| Docker packaging, `.dockerignore`, Makefile, runtime packaging probe | Both domains plus build/startup smoke of Linux amd64 image | Yes, for ready candidates |
| Explicit documentation paths | Cheap planner and aggregate | No |

PR planning includes both the increment against the declared base's merge base
and changes introduced from PR head to the checked-out merge tree. Missing
history or a checkout mismatch fails planning. Master pushes classify the whole
`before..after` range. The summary records base/head/merge/tree identities and
selected groups. Unknown paths select all test domains, not an image.

Drafts defer substantive checks and report the separate informational
`validation-deferred` context. Becoming ready starts the real `ci-required` job
immediately, before planning and tests, so the required context stays pending
through validation rather than waiting on a dependent aggregate. It verifies
planner success, valid domain coverage, generation, selected tests and compilation,
and any required image build/smoke. Skipped selected steps, failures and cancelled
runs cannot satisfy it. Superseded PR runs alone are cancelled. Master protection
requires `ci-required`; this patch does not change repository settings.

Deliberate release/manual runs can reuse backend, frontend or Python tests from
one successful master push at the exact commit and workflow revision. The probe
compares immutable workflow bytes, repository/ref/event identity, completed run
attempt, and successful classifier/aggregate/domain steps. The summary records
source run/attempt, commit/workflow revision, workflow digest and domain coverage.
Unproven domains execute normally; missing, pending, failed, stale, mismatched or
inaccessible evidence triggers normal execution. PR checks, tree equality and
nearby commits are insufficient for release test reuse. Ordinary PR/master tests
still execute their selected domains.

For a packaging-changing PR, a successful real build/smoke can record a small
seven-day evidence artifact containing identities, hashes, resolved base-image
materials and a GitHub-signed workflow/job receipt. A normal two-parent master merge may reuse that packaging validation
only when the same-repository merged PR, ordered base/head parents, synthetic
merge tree, immutable workflow, declared variable inputs and selected coverage
match. The receipt's signature and proof-hash audience independently bind the
actual source workflow/checkout, PR ref, run/attempt and job; artifact assertions
alone cannot authorize reuse. Historical receipts must have been valid during the
completed source job, and unavailable signing keys force a rebuild. Registry reads
must confirm that mutable base tags still resolve to the
source build's actual material digests. The current run also checks its own landed
checkout identity and runs master tests/generation normally. The final gate accepts
skipped build/smoke only with this complete proof and records the source run/attempt,
candidate/landed commits and input/workflow hashes. Unsupported or absent source
provenance, expired artifacts, squash/rebase landings, changed inputs, stale run
attempts and API/registry failures rebuild normally. Branch protection is unchanged.
The proof confirms the same declared recipe/variables and current registry base
materials; it does not re-resolve every APK/npm repository response or tool download.
It reuses recent validation evidence, not binary equivalence or a freshly built image.

The explicitly approved `id-token: write` permission applies to the entire
`ci-required` job across its existing triggers. Only successful same-repository PR
packaging validation requests the proof-bound receipt. Receipt values and request
errors are never logged, and no token is exchanged with a cloud provider or cloud
trust configured. Missing receipt access still makes master packaging build normally.
The publishing job receives no new permission; ordinary GitHub credentials,
branch rules and the published release are unchanged.

This reuses packaging evidence, never a synthetic-merge image. Its commit/version/
date metadata is deliberately distinct from landed master; no image is represented
as built for the landed SHA. Tag and manual candidates always build and smoke-test
their own final image, including release identity and PostHog UI maps/chunk IDs.

For image candidates, the existing Docker stages compile each production output
once and use those outputs in the final image. Host production compilation is
omitted in that case; ordinary non-image validation still compiles on the host.
An ordinary validation binary is never promoted into a release: release metadata,
hidden source maps/chunk IDs remain release-specific. Validation Docker builds
already retain native symbols; publication uploads those from its exact release
binary. Native symbols alone are not a reason to repeat an equivalent build.

## Existing commands

Use Python 3.11 or newer for the existing packaging regression tests. No dependency
installation is needed for these fast offline checks:

```sh
python3 -m unittest discover -s scripts -p test_image_publication_policy.py
python3 -m unittest discover -s scripts -p test_release_version.py
python3 -m unittest discover -s scripts -p test_check_intel_runtime.py
python3 -m unittest discover -s scripts -p test_reuse_master_validation.py
python3 -m unittest discover -s scripts -p test_reuse_pr_packaging.py
```

For a candidate affecting both application domains, the existing commands are:

```sh
export POSTHOG_UPLOAD_REQUIRED=false
make pre-ui generate generate-login-locale
# Same test targets as make test / make it, with cached results disabled.
go test -count=1 ./...
go test -count=1 -tags 'sqlite_stat4 sqlite_math_functions integration' ./...
(cd ui/v2.5 && pnpm test && pnpm run check)
make ui-only
make flags-release flags-pie stash
```

For backend-only changes, use `make generate-backend generate-login-locale`, the
same two Go test commands, and `make flags-release flags-pie stash`. The existing
`touch-ui` generation prerequisite provides the embed placeholder: this compile
checks backend linking, not a complete UI deliverable. For frontend-only changes,
use `make pre-ui generate-ui`, the frontend test/type commands, and `make ui-only`.
Python changes use `python3 -m unittest discover -s scripts -p 'test_*.py'`; when
present, also run the existing suite in `scripts/gpu_generation`.

`pnpm run validate` remains the documented lint/type/format acceptance command.
This workflow retains the existing test/type checks and adds production
compilation; it does not claim a clean lint/format baseline that has not been run.

These commands can download modules/packages and use CPU, memory and disk for
code generation, tests and native compilation. They do not build/push an image or
upload symbols. Freeze the candidate and record its commit/tree plus any remaining
patch before push; later relevant changes require renewed evidence.

## Packaging and deliberate delivery

Use the existing `make docker-build` only after affected checks pass and packaging
has changed, or an integrated image candidate/release is deliberately selected.
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
CI explicitly supplies the planned version to the backend and UI. Native symbol
uploads retain the exact release binary's build ID and existing symbol settings.

For a release, bump `VERSION` in the reviewed candidate, land it on master, then
tag that exact commit `v<the VERSION value>`. Tag planning rejects a different
version or a commit outside master. The exact final image is built/smoke-tested
once and published as `:vMAJOR.MINOR.PATCH`, `:sha-<full SHA>` and `:latest`.
The user's existing Compose `latest` reference therefore keeps working. Our
publishers serialize and refuse to replace an existing version tag, including
reruns; a new release needs a new version. Registry/auth/network uncertainty
refuses publication. This is workflow enforcement, not a registry ruleset change.

Manual workflow dispatch explicitly selects a full build/smoke even without
publication. Both publication options require master and explicit opt-in; they
are mutually exclusive and default off. `publish_latest` uses the same release
version/tag policy as a tagged release. `publish_test_image` uses the separate
test package, an opaque `test-<SHA prefix>-<run>-<attempt>` transport tag and the
development application version. The optional diagnostic `test_label` affects
neither version nor tag. Ordinary PR/master validation never publishes images.
No image dispatch is needed to validate changes to version planning.

The publication summary records image references and repository digests.
Release versions are baked before the build; retagging a development image
cannot change its application version.
Until a trusted local-to-CI artifact intake exists, deliberate CI publication
keeps the existing same-run build/smoke/archive/checksum handoff. The isolated
publisher checks the smoke-tested image ID, checksums and revision/version/platform, then publishes those bytes without
checkout or rebuilding. No arbitrary build artifact intake or general cache framework is introduced.

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
when integrating branches. Reconcile deliberate publication's upload build args
separately; workflow environment variables alone do not become Docker build args.

The checked-in Compose template names the same application package but defaults
`IMAGE_TAG` to `edge`, with no explicit `pull_policy`. The user's existing
production Compose is reported to use `latest`; it has not been inspected or
changed by this task. Use that existing configuration for the deliberate latest
candidate, and verify its effective pull behavior before expecting cached-image
refresh. No production deployment is performed here.
