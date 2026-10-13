# Sonar source classification and coverage

`sonar-project.properties` is a **CI-based scanner** configuration. It does not
activate a scanner or change the existing required `SonarCloud Code Analysis`
check, merge protections, or quality gate. No coverage thresholds are added.

## Analysis scope

The project's new analysis-scope UX analyzes the whole repository by default.
The scanner reported that `sonar.sources`, `sonar.tests`, `sonar.test.inclusions`
and `sonar.test.exclusions` have no effect; those four properties are removed.
Effective source/test classification must be verified in the Sonar project.
Removing ignored properties does not establish test classification or coverage
visibility, and tests/support are not moved into global exclusions.

`sonar.exclusions` retains the existing dependency directories (`node_modules`,
`vendor`), built UI assets, generated login locales and exact generated code
outputs. Generated exclusions are tied to `gqlgen.yml`, `ui/v2.5/codegen.ts`, the
stash-box client generator, dataloaden headers and mockery headers. Only mockery's
`*ReaderWriter.go` outputs are excluded; handwritten `database.go`, `query.go`
and `pkg/models/paths/paths_generated.go` stay available for analysis. The 77
verified binary paths are grouped into directory/extension patterns. The tracked
file matches are unchanged; font licenses, SVGs and other application text remain
available for analysis. Dependency/generated exclusions are unchanged.

The 102 SQLite migration scripts (`pkg/sqlite/migrations/*.sql`) are excluded:
Sonar analyzes every `.sql` file as Oracle PL/SQL, which is the wrong dialect and
warns about a missing Oracle Data Dictionary. The Go pre/post-migration code in the
same directory stays analyzed. `sonar.python.version` is `3.12, 3.13, 3.14`, from
the ubuntu-24.04 runner's system Python (3.12) to the `alpine:3.24` image (3.14).

Build/cache and coverage directories (`dist`, `.local*`, etc.) are Git-ignored;
they are not outside configured source roots, because those roots no longer
exist. Effective server scope remains to be verified. Vitest's separate coverage
configuration still excludes TypeScript declarations, tests, test support and
`generated-graphql.ts` from instrumentation. Declarations and tests remain
available for static analysis.

## Report production

The existing `ci-required` validation runs generate reports without repeating
the suites:

```sh
mkdir -p dist/coverage
go test -count=1 -covermode=atomic -coverprofile=dist/coverage/go-unit.out ./...
go test -count=1 -covermode=atomic -coverprofile=dist/coverage/go-integration.out \
  -tags 'sqlite_stat4 sqlite_math_functions integration' ./...
(cd ui/v2.5 && pnpm run test:coverage && pnpm run check)
scripts/python_coverage.sh
```

Use the existing generation/dependency steps before these commands. The frontend
coverage script preserves the Node build-config tests and runs Vitest once with
V8 coverage. Vitest includes untested `src/**/*.{ts,tsx}` files and writes
`dist/coverage/frontend/lcov.info`; it does not measure the Node build-tool scripts.
`scripts/python_coverage.sh` runs the Python regression suites (`scripts/` and
`scripts/gpu_generation/`) under coverage.py, which is installed with hash pinning
from `scripts/coverage-requirements.txt` into a venv that keeps the runner's
system PyYAML. `scripts/coverage.ini` measures all of `scripts/` and writes
repository-relative paths, so `dist/coverage/python.xml` (Cobertura) resolves from
Sonar's `sonar-source` checkout. The hyphenated `scripts/ffmpeg/build-*.py` image
build scripts are not importable and are not measured. There is no fabricated
report for build scripts or other languages.

`sonar.go.coverage.reportPaths` takes the two native Go coverprofiles. Do not
concatenate them or treat them as generic coverage XML. The tagged suite also
executes unit tests; importing both reports preserves both observations without
creating a third test run. `sonar.javascript.lcov.reportPaths` takes LCOV for
both JavaScript and TypeScript, and `sonar.python.coverage.reportPaths` takes the
Cobertura XML. Run the scanner from the repository root; LCOV
source entries must resolve to `ui/v2.5/src/...`, not a different checkout path.

Reports are required to be nonempty after successful tests and retained for
seven days as `go-coverage` / `frontend-coverage` / `python-coverage` artifacts. These are evidence,
not an automatic input to the remote Sonar service. Each artifact contains only
the domains actually executed in that run. Drafts run selected validation automatically;
unselected or reused domains do not produce new reports. Never scan a partial
run as though it contains full-project coverage. All reports live under `dist/coverage/`, which is already ignored by Git and
Docker packaging. No ignore-file or image-planner change is needed.

## CI analysis and acceptance

The user confirmed `SONAR_TOKEN` is configured in GitHub and Automatic Analysis
is disabled. Project `rzp-labs_vexxx-stash`, organization `rzp-labs`, and host
`https://sonarcloud.io` are configured. No token value is stored in the repository
or exposed to validation. The official pinned scanner runs only on the existing
GitHub-hosted runner, after validation; no Sonar CLI administration or scanner
installation is required on the Mini.

Same-repository draft and ready PRs retain their actual target branch, including
stacks. Full history is fetched and the scanner uses the actual HEAD checkout.
All PR scans, including scans without coverage reports, require the complete HEAD
Git tree to equal the tested merge-result Git tree. A merge-only difference requires
updating the branch before scanning. Forks and missing trust metadata fail fresh
validation without receiving the token. Tags and publication dispatches retain
their existing validation/publication policy and do not run Sonar.

PR scans import only their selected Go/UI reports. A prior completed native run
may supply those reports when independent Git input/workflow/variable comparison,
actual generation/test/production-compile results (or a real image build/smoke
with skipped host compiles), fresh attempts and artifact
digests prove equivalence. Only `sonar.exclusions` may differ for that validation
reuse; a changed exclusion list always scans again. Reused reports retain their
original provenance and are not uploaded as newly produced coverage.

The scanner waits for its actual quality gate. After it succeeds, CI records its
real analysis task and a fresh successful `SonarCloud Code Analysis` result from
app ID 12526 for the actual head and PR. Ready events require that completed scan's
exact head/base/target/full tree/configuration and independently verified native
validation, then reuse clearance. A draft packaging change may record valid native/Sonar clearance before its final
packaging guard fails the required job. Only that sole, expected guard failure can
qualify a scan donor; the ready run must complete the existing image build/smoke.
Other PR events scan again. Pending mergeability receives bounded retries while
head/base/target identities are rechecked. Current GitHub PR
metadata is checked before work, before scan and after clearance. This cannot
atomically prevent a base change during merge; server-side strict up-to-date
protection or checks on a merge-queue group SHA remain necessary.

Missing or empty selected reports fail. Same-run dependencies and generated
GraphQL types are reused when available after input equality, without another
install. Frontend type precision when dependencies are absent remains a live
question. No partial PR import is presented as full-project coverage.

A master push changing analyzed inputs imports both Go profiles, frontend LCOV and
Python Cobertura XML. Separate coverage-only flags add missing report producers
without changing affected validation obligations: each unselected domain among
backend, frontend and Python gets a coverage-only run (Go with setup/generation,
Vitest with setup/generation, `scripts/python_coverage.sh`). These additions
perform no standalone TypeScript check, Node build-config tests, production compile
or image build. Genuine affected validation retains those checks. Documentation-only
master changes and explicitly excluded inputs add no producer work or scan; other
repository inputs follow the whole-project scope. Workflow changes already select
all three domains. PR reuse follows the narrow
independent evidence checks above.

The snippet's analyzer cache is used once at `~/.sonar/cache`. It is separate from
the action's scanner-binary tool cache. Existing checkout pins, full history, required `SonarCloud Code Analysis` identity, and merge protections are
preserved. The commented placeholder build/test job is not introduced.

The scanner checkout must not live under a dot-directory. Early runs used
`.sonar-source` as `projectBaseDir`; the scanner marks every file below a hidden
base directory as hidden, and only sensors that opt into hidden files (IaC, text
and secrets) received them. Go, JavaScript/TypeScript, CSS and Python sensors were
skipped, so the project reported 851 IaC lines across 9 files and imported no
coverage. The checkout is now `sonar-source`, and the workflow test rejects any
hidden path component in `projectBaseDir`.

Before marking VEX-119 complete, inspect real PR and master scanner logs for Go/LCOV
imports and unresolved paths, compare representative covered/uncovered files and
line/branch totals with reports, and verify the existing Sonar app publishes the
required context for the actual head and target. Go statement coverage is not
numerically identical to Sonar line coverage. Local checks do not establish live
coverage visibility. With Automatic Analysis off, new/updated PRs rely on this CI
integration for fresh Sonar results; an existing same-SHA result may remain visible.

Official references:

- [Automatic analysis limitations and CI conflict](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/automatic-analysis)
- [Initial source/test scope](https://docs.sonarsource.com/sonarqube-cloud/managing-your-projects/project-analysis/setting-analysis-scope/setting-initial-scope)
- [Go coverage](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/test-coverage/go-test-coverage)
- [JavaScript/TypeScript LCOV](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/test-coverage/javascript-typescript-test-coverage)
