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

The `ci.yml` domain jobs generate reports as part of their normal runs:

```sh
mkdir -p dist/coverage
go test -count=1 -covermode=atomic -coverprofile=dist/coverage/go.out \
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
repository-relative paths, so `dist/coverage/python.xml` (Cobertura) resolves
against the scanner's repo-root checkout. The hyphenated `scripts/ffmpeg/build-*.py` image
build scripts are not importable and are not measured. There is no fabricated
report for build scripts or other languages.

`sonar.go.coverage.reportPaths` takes the single native Go coverprofile from the
integration-tagged run, which also executes every untagged unit test. Do not
treat it as generic coverage XML. `sonar.javascript.lcov.reportPaths` takes LCOV for
both JavaScript and TypeScript, and `sonar.python.coverage.reportPaths` takes the
Cobertura XML. Run the scanner from the repository root; LCOV
source entries must resolve to `ui/v2.5/src/...`, not a different checkout path.
`sonar.coverage.exclusions` lists `ui/v2.5/vite.config.js`: build tooling outside
that LCOV scope can never report coverage, but it is still analyzed for issues.
It also lists Go `*_test.go`, Vitest `*.test.ts(x)` and `*.spec.ts(x)`, the Node-run
`ui/v2.5/scripts/**/*.test.mjs`, `ui/v2.5/src/test/**` and Python
`scripts/**/test_*.py`. Sonar classifies these as main sources, so they
otherwise count as lines to cover: Go, Vitest and Node tests are never
instrumented and appear fully uncovered, while coverage.py measures Python
tests as covered. Excluding them keeps the new-code gate on production code.
They remain analyzed for issues.

Reports are required to be nonempty after successful tests and are uploaded as
`go-coverage` / `frontend-coverage` / `python-coverage` artifacts for the `sonar`
job to import. All reports live under `dist/coverage/`, which is already ignored
by Git and Docker packaging.

## CI analysis and acceptance

The user confirmed `SONAR_TOKEN` is configured in GitHub and Automatic Analysis
is disabled. Project `rzp-labs_vexxx-stash`, organization `rzp-labs`, and host
`https://sonarcloud.io` are configured. No token value is stored in the repository
or exposed to validation. The official pinned scanner runs only on the existing
GitHub-hosted runner, after validation; no Sonar CLI administration or scanner
installation is required on the Mini.

Same-repository PRs retain their actual target branch, including stacks. Full
history is fetched and the scanner uses the actual HEAD checkout. The merge
queue runs its own `ci-required` jobs before landing. Forks do not receive the
token. Release runs do not scan.

The `sonar` job downloads the `go-coverage`, `frontend-coverage` and
`python-coverage` artifacts only from the domain jobs that succeeded in that
run, then scans and waits for its actual quality gate. It does not run on
`merge_group` (the `master` push covers the landed tree). The `SonarCloud Code
Analysis` app check on `master` remains required alongside `ci-required`.
Domain jobs require their report to be nonempty; no partial-domain import is
presented as full-project coverage.

A master push imports the Go profile, frontend LCOV and Python Cobertura XML
produced by that push's `ci.yml` run.

The `sonar` job checks out the repository root with full history and relies on
the scan action's own caches; there is no separate analyzer-cache or
`projectBaseDir` configuration. Historical note: an earlier workflow checked
out under a hidden `.sonar-source` directory, which made the scanner treat
every file as hidden and skip the Go/JavaScript/CSS/Python sensors. Scanning
from the repo root avoids that failure mode.

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
