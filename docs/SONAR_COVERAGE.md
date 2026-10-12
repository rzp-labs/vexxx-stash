# Sonar source classification and coverage

`sonar-project.properties` is a **CI-based scanner** configuration. It does not
activate a scanner or change the existing required `SonarCloud Code Analysis`
check, merge protections, or quality gate. No coverage thresholds are added.

## Analysis scope

The scope includes all Go commands, backend packages, UI sources, repository
tools, GraphQL inputs, Docker configuration and workflow configuration. Go
`*_test.go`, frontend `*.test.{ts,tsx}` / `*.spec.{ts,tsx}`, Node `*.test.mjs`,
Python `test_*.py`, test fixtures, frontend setup/helpers and the test database
generator are classified as tests. Sonar automatically removes test inclusions
from the main source scope, making the two final scopes disjoint.

Exclusions identify dependency directories (`node_modules`, `vendor`), built UI
assets, coverage output and generated login locales. Generated code exclusions
are tied to `gqlgen.yml`, `ui/v2.5/codegen.ts`, the stash-box client generator,
dataloaden headers and mockery headers. Handwritten mock helpers
`pkg/models/mocks/database.go` and `query.go`
are classified as tests, while only mockery's `*ReaderWriter.go` outputs are
excluded. Hand-written production code stays in
scope, including `pkg/models/paths/paths_generated.go`: its name refers to media
paths and is not evidence of generated source. Build/cache directories at the
repository root (`dist`, `.local*`, etc.) are outside the explicit source roots.
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
the domains actually executed in that run. Drafts defer substantive validation;
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

Same-repository non-draft PRs retain their actual target branch, including stacks.
Full history is fetched, and the scanner uses a separate actual-HEAD checkout;
existing validation continues against the merge tree. The real Sonar service must
have a suitable target-branch baseline; there is no override to master or synthetic
successful check. Forks/missing trust metadata fail fresh `ci-required` validation
without receiving the token. Fork contribution routing needs a maintainer-approved
policy before activation; the local candidate does not introduce a privileged
fork-code execution route. Drafts defer substantive validation and scanning;
ready-for-review and retarget events start a fresh required run at the same SHA.
Tags and publication dispatches retain their existing publication/validation policy
and do not run Sonar.

PR scans import only the Go/UI reports produced by their selected domains. Static
PR scans with no such reports verify the scan checkout but do not require merge/head
equivalence. Any copied reports require identical analyzed/test/build inputs between
tested merge and source HEAD; missing or empty selected reports fail. No partial PR
import is presented as full-project coverage. Same-run dependencies and generated
GraphQL types are reused when available after input equality, without another install.
Sonar-specific precision when frontend types are absent remains a live-pilot question.

A master push changing analyzed inputs imports both Go profiles and frontend LCOV.
Separate coverage-only flags add missing report producers without changing affected
validation obligations: backend-only changes add Vitest coverage with setup/generation;
frontend-only changes add the two Go report runs with setup/generation; Python-only
analyzed changes add both. These additions perform no standalone TypeScript check,
Node build-config tests, production compile or image build. Genuine affected validation
retains those checks. Documentation-only master changes and CI inputs outside the
analysis scope add no producer work or scan; workflow changes already select both
normal domains. There is no cross-revision report-cache machinery.

The snippet's analyzer cache is used once at `~/.sonar/cache`. It is separate from
the action's scanner-binary tool cache. Existing checkout pins, full history, draft
behavior, required `SonarCloud Code Analysis` identity, and merge protections are
preserved. The commented placeholder build/test job is not introduced.

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
