# Sonar source classification and coverage

`sonar-project.properties` is a **CI-based scanner** configuration. It does not
activate a scanner or change the existing required `SonarCloud Code Analysis`
check, merge protections, or quality gate. No coverage thresholds are added.

## Analysis scope

The scope includes all Go commands, backend packages, UI sources, repository
tools, GraphQL inputs, Docker configuration and workflow configuration. Go
`*_test.go`, frontend `*.test.{ts,tsx}` / `*.spec.{ts,tsx}`, Node `*.test.mjs`,
Python `test_*.py`, test fixtures, frontend setup/helpers and the test database
generator are configured as tests. Under the documented legacy scope rules,
Sonar removes test inclusions from main scope. Live classification must still be
verified: the first merged run reported that its new analysis-scope UX ignores
`sonar.sources`, `sonar.tests` and test inclusion/exclusion properties. Repository
intent and local classification checks do not establish effective server scope.

Exclusions identify dependency directories (`node_modules`, `vendor`), built UI
assets, coverage output and generated login locales. Generated code exclusions
are tied to `gqlgen.yml`, `ui/v2.5/codegen.ts`, the stash-box client generator,
dataloaden headers and mockery headers. Handwritten mock helpers
`pkg/models/mocks/database.go` and `query.go`
are classified as tests, while only mockery's `*ReaderWriter.go` outputs are
excluded. Hand-written production code stays in
scope, including `pkg/models/paths/paths_generated.go`: its name refers to media
paths and is not evidence of generated source. Build/cache directories at the
repository root (`dist`, `.local*`, etc.) are outside the configured source roots;
the live scope warning above prevents assuming that exclusion is effective.
Vitest additionally excludes TypeScript declarations, tests, test support and
the actual `generated-graphql.ts` output from instrumentation. Declarations
remain available for static analysis.

## Report production

The existing `ci-required` validation runs generate reports without repeating
the suites:

```sh
mkdir -p dist/coverage
go test -count=1 -covermode=atomic -coverprofile=dist/coverage/go-unit.out ./...
go test -count=1 -covermode=atomic -coverprofile=dist/coverage/go-integration.out \
  -tags 'sqlite_stat4 sqlite_math_functions integration' ./...
(cd ui/v2.5 && pnpm run test:coverage && pnpm run check)
```

Use the existing generation/dependency steps before these commands. The frontend
coverage script preserves the Node build-config tests and runs Vitest once with
V8 coverage. Vitest includes untested `src/**/*.{ts,tsx}` files and writes
`dist/coverage/frontend/lcov.info`; it does not measure the Node build-tool scripts.
There is no fabricated report for Python, build scripts, or other languages.

`sonar.go.coverage.reportPaths` takes the two native Go coverprofiles. Do not
concatenate them or treat them as generic coverage XML. The tagged suite also
executes unit tests; importing both reports preserves both observations without
creating a third test run. `sonar.javascript.lcov.reportPaths` takes LCOV for
both JavaScript and TypeScript. Run the scanner from the repository root; LCOV
source entries must resolve to `ui/v2.5/src/...`, not a different checkout path.

Reports are required to be nonempty after successful tests and retained for
seven days as `go-coverage` / `frontend-coverage` artifacts. These are evidence,
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
actual generation/test/production-compile results, fresh attempts and artifact
digests prove equivalence. Only `sonar.exclusions` may differ for that validation
reuse; a changed exclusion list always scans again. Reused reports retain their
original provenance and are not uploaded as newly produced coverage.

The scanner waits for its actual quality gate. After it succeeds, CI records its
real analysis task and a fresh successful `SonarCloud Code Analysis` result from
app ID 12526 for the actual head and PR. Ready events require that completed scan's
exact head/base/target/full tree/configuration and independently verified native
validation, then reuse clearance. Other PR events scan again. Current GitHub PR
metadata is checked before work, before scan and after clearance. This cannot
atomically prevent a base change during merge; server-side strict up-to-date
protection or checks on a merge-queue group SHA remain necessary.

Missing or empty selected reports fail. Same-run dependencies and generated
GraphQL types are reused when available after input equality, without another
install. Frontend type precision when dependencies are absent remains a live
question. No partial PR import is presented as full-project coverage.

A master push changing analyzed inputs imports both Go profiles and frontend LCOV.
Separate coverage-only flags add missing report producers without changing affected
validation obligations: backend-only changes add Vitest coverage with setup/generation;
frontend-only changes add the two Go report runs with setup/generation; Python-only
analyzed changes add both. These additions perform no standalone TypeScript check,
Node build-config tests, production compile or image build. Genuine affected validation
retains those checks. Documentation-only master changes and CI inputs outside the
analysis scope add no producer work or scan; workflow changes already select both
normal domains. Master coverage-only producers remain unchanged; PR reuse follows the narrow
independent evidence checks above.

The snippet's analyzer cache is used once at `~/.sonar/cache`. It is separate from
the action's scanner-binary tool cache. Existing checkout pins, full history, required `SonarCloud Code Analysis` identity, and merge protections are
preserved. The commented placeholder build/test job is not introduced.

The first merged run retained genuine Go and LCOV artifacts and passed its quality
gate, but logged no Go or JavaScript/TypeScript coverage sensors. Coverage ingestion
remains unresolved; absent server metrics are not zero coverage. This follow-up
has not established a server-side cause or justified an authentication change.

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
