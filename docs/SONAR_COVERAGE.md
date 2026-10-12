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

## Activation prerequisite and acceptance

There is currently no repository scanner invocation. The existing master check
confirms project key `rzp-labs_vexxx-stash`, the EU host `sonarcloud.io`, and the
`SonarCloud Code Analysis` context from app `sonarqubecloud` (ID 12526). At base
`d791f6154d367ca97a4945847cbaf57fecc980f9`, it succeeded and reported 0.0% coverage
on new code. A separate read-only setup inspection confirmed the organization
key `rzp-labs` and public project. The public UI shows no coverage and warns that
`sonar.tests` is not configured. Analysis Method remains unverified because its
page requires login; automatic analysis is plausible, not established.

Names-only GitHub metadata inspection found one repository secret, three
organization secrets, zero environments, and no names matching `SONAR`.
Unusually named credentials remain possible. No secret values were read; names
alone do not prove token validity or Execute Analysis permission. The remaining
administrator handoff is the authenticated Analysis Method check and identifying
an existing suitable CI credential or resolving its absence separately. No
credential creation or server change is authorized by this candidate.
If the project uses automatic
analysis, stop for a separately authorized server-side change: automatic analysis
does not ingest coverage and ignores `sonar-project.properties`. CI analysis and
automatic analysis must not run together.

After those prerequisites are resolved, wire the scanner **after existing test
execution**, on representative PR and default-branch runs that produce both Go
profiles and LCOV. The affected-domain planner and exact-master reuse must be
accounted for: either obtain complete reports from the exact analyzed revision
with proven provenance or select both existing test domains before scanning.
Do not add an independent duplicate suite, silently skip the required check,
consume stale reports, or modify protection rules. Keep the current required
check name and verify its successful publication by the existing Sonar app.

Before marking VEX-119 complete, inspect scanner logs for successful Go/LCOV
imports and unresolved paths; compare source/test inventory to this scope;
compare representative covered and uncovered files and line/branch totals
against the actual reports in both PR and master analyses. Go tooling reports
statement coverage, while Sonar reports line coverage; a numeric equality of
those percentages is not an acceptance requirement. Local report generation
alone does not establish server ingestion or coverage visibility.

Official references:

- [Automatic analysis limitations and CI conflict](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/automatic-analysis)
- [Initial source/test scope](https://docs.sonarsource.com/sonarqube-cloud/managing-your-projects/project-analysis/setting-analysis-scope/setting-initial-scope)
- [Go coverage](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/test-coverage/go-test-coverage)
- [JavaScript/TypeScript LCOV](https://docs.sonarsource.com/sonarqube-cloud/analyzing-source-code/test-coverage/javascript-typescript-test-coverage)
