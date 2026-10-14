# Vexxx repository guidance

Vexxx is a Stash fork. Prefer extensions alongside upstream code where practical
so upstream merges remain tractable. Read the relevant implementation before edits.
For reviews, follow [REVIEW.md](REVIEW.md); classification does not grant tool permissions.

## Layout and sources of truth

- Backend: `pkg/`, `internal/`, and `cmd/stash/`; Go version is declared in `go.mod`.
- UI: `ui/v2.5/src/`; React/TypeScript, Vite, and Vitest versions/scripts are in
  `ui/v2.5/package.json`. Do not assume React18 or Jest from older fork guidance.
- SQLite: `pkg/sqlite/`, with repository interfaces in `pkg/models/`. Follow existing
  transaction and migration patterns rather than introducing a parallel data path.
- GraphQL: `graphql/schema/` is the source; resolvers live in `internal/api/`.
  Regenerate after schema/operation changes and rebuild the backend when needed.
  Use the generated `src/core/generated-graphql` hooks for frontend API operations.
- Detailed conventions: [contribution guide](docs/CONTRIBUTING.md),
  [development](docs/DEVELOPMENT.md), and [GraphQL workflow](docs/GQL_TECHNICAL_DOC.md).
- Knowledge store: `docs/solutions/` holds documented solutions to past problems (bugs, best practices, workflow patterns), organized by category with YAML frontmatter (`module`, `tags`, `problem_type`).

## Development and verification

From the repository root:
- `go test ./...`: backend tests; `make it`: integration tests.
- `make stash`: backend build; `make generate`: GraphQL generation.

From `ui/v2.5/`:
- `pnpm run dev`: backend and Vite together; `pnpm run start`: frontend only.
- `pnpm run check`: TypeScript; `pnpm run validate`: lint/type/format checks.
- `pnpm test`: Vitest; `pnpm run build`: frontend build.

Run checks appropriate to the change and report their results and limitations.
The existing browser-verification policy uses these checks plus explicit manual
click/select steps for the user. Do not search for or install headless browser
tooling under this policy. Do not assume a development server is already running.

After a solved, verified problem, offer once to invoke the `ce-compound` skill at the completion checkpoint only when the work produced durable project reasoning that is not readily recoverable from the final code, tests, types, comments, or existing documentation, and losing it would plausibly cause recurrence, material risk, or substantial rediscovery. Apply this counterfactual: if the learning document disappeared, would a future engineer reading the final implementation still be likely to repeat the mistake or redo substantial investigation? If not, do not offer. Completion, effort, and diff size alone are not enough. Offer at the checkpoint so a qualifying learning can ship in the PR that produced it, and only where the repository treats captured learnings as tracked, committed knowledge.

Write every report, summary, or handoff to the user through the `ce-noslop` skill. This applies when you are the top-level agent writing to the user, not when you are a subagent reporting to its caller. Do not apply it to code, config, verbatim quotes, or text the user asked to post as written.

## Frontend conventions

- Use MUI structural components when layout needs theme tokens (`theme.palette`,
  `sx`). Use Tailwind for custom components that own their full visual style.
  Do not mix the two styling systems within one component.
- Wrap new top-level page/card components with `PatchComponent`; do not wrap
  utility/shared components merely to follow that convention.
- Non-GraphQL UI preferences use `useInterfaceLocalForage`. Follow surrounding
  access patterns, including existing `any`/`@ts-ignore` exceptions where needed.
- Settings use `SelectSetting` / `BooleanSetting` in
  `ui/v2.5/src/components/Settings/Inputs.tsx`. Supply a valid `headingID` locale key
  from `ui/v2.5/src/locales/en-GB.json` for setting headings.
- Scene-card variants use localForage `sceneCardTheme` (`overlay`, `flip`, `stashdb`,
  `cinema`). Add the component, import/selection in `SceneCard.tsx`, an option in
  `SettingsInterfacePanel.tsx`, and its locale key when adding a variant.

## Git policy

When the user asks for a commit message, **only output the message text** — do not stage files or run `git commit`. Leave all git operations (add, commit, push, branch, etc.) to the user.
Do not infer Git authorization from review classification or routine coding work.

## Cursor Cloud specific instructions

- The image Go is 1.22 and `node` on `PATH` may be 22. This repo needs Go 1.26 (`go.mod`) and Node 24 (CI). The environment install puts Go 1.26.9 in `/usr/local/go` and Node 24.21.0 plus pnpm 10.33.4 in `/usr/local/node`. `/etc/profile.d/vexxx-dev.sh` prepends those directories and sets `GOTOOLCHAIN=local`. Non-login shells need the same `PATH` or they will build with the wrong toolchain.
- Before the app will compile, run `make pre-ui` and `make generate`. `ui/v2.5/src/core/generated-graphql.ts` and `internal/api/generated_*.go` are gitignored.
- Dev loop: `make server-start` serves the API on port 9999 with config under `.local/`. `make ui-start` serves Vite on port 3000. In dev, `getPlatformURL` sends GraphQL HTTP and WebSocket requests to port 9999 on the page host, or to `VITE_APP_PLATFORM_URL` when that is set, so the browser needs port 9999 as well as port 3000. Session cookies are cross-origin in that setup, so authentication stays off for the dev UI. `ffmpeg` is on the image and is required to scan video.
- A fresh server reports GraphQL `systemStatus` `SETUP` until the database and generated paths are saved. An empty library is allowed; the setup wizard warns and continues after confirmation.
