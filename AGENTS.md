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

## Claude Code context management

After a discrete task, proactively suggest `/compact` if the conversation is
getting long, including multi-file reads, a build/test cycle, or roughly 10 tool calls.
Suggest `/clear` when switching to a completely different area of the codebase.
