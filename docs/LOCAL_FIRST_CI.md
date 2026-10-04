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

Drafts defer substantive checks and deliberately fail `ci-required` with a clear
summary. Becoming ready runs fresh confirmation. The aggregate verifies planner
success, valid domain selections, and success of selected jobs; an unselected job
must be skipped. Superseded PR runs alone are cancelled. This check is not yet a
required branch-protection check; repository settings are unchanged.

## Existing commands

Use Python 3.11 or newer for the existing packaging regression tests. No dependency
installation is needed for these fast offline checks:

```sh
python3 -m unittest discover -s scripts -p test_image_publication_policy.py
python3 -m unittest discover -s scripts -p test_check_intel_runtime.py
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

Manual workflow dispatch explicitly selects a full candidate build/smoke even
without publication. Both publication options require master, a valid candidate/test label and an
explicit opt-in. `publish_test_image` uses the separate test package.
`publish_latest` instead publishes the exact tested image to a unique
`candidate-<label>-<run>-<attempt>` tag in `ghcr.io/rzp-labs/vexxx-stash`, then tags
and pushes those same bytes as `:latest`. The candidate tag is also the baked
application version; no release version is invented. The options are mutually
exclusive and default off. Ordinary master landings and normal releases do not
update latest. The publication summary records image references and repository
digests. Normal release tags must be exact `vMAJOR.MINOR.PATCH` and their
commit must be reachable from master. Release/manual versions are baked before
the build; retagging a development image cannot change its application version.
Until a trusted local-to-CI artifact intake exists, deliberate CI publication
keeps the existing same-run build/smoke/archive/checksum handoff. The isolated
publisher checks revision/version/platform and publishes those bytes without
checkout or rebuilding. No cross-run result cache or arbitrary artifact intake
is introduced.

## PostHog branch integration

The separate `feat/posthog-instrumentation` branch is not part of this patch.
At `6801148`, Vite's actual upload condition includes
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
