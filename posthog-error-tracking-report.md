# PostHog Error Tracking

## GitHub Actions configuration completed

With explicit user approval, `gh` configured the following repository Actions settings in `rzp-labs/vexxx-stash` using the existing local wizard values:

- Secret: `POSTHOG_CLI_API_KEY`
- Variables: `POSTHOG_CLI_PROJECT_ID`, `POSTHOG_CLI_HOST`, `VITE_PUBLIC_POSTHOG_PROJECT_TOKEN`, and `VITE_PUBLIC_POSTHOG_HOST`

GitHub accepted the secret write and its presence was verified. All four variables were read back and matched the local values; the configured PostHog project is 644503. The CLI host is the app/API host; the public SDK host is the ingestion host. The frontend and backend build stages reuse the same upload secret and project ID. For future key rotation, use the **Source map upload** preset at https://us.posthog.com/settings/user-api-keys.

## What you still need to do

At production Compose startup, provide the runtime environment variables `POSTHOG_PROJECT_TOKEN` and `POSTHOG_HOST` in the deployment environment/root Compose `.env`. A production image build and a real browser error event still need end-to-end verification.

Local dependencies are already installed. To refresh them, use `make pre-ui`. The frontend uploader brings its own pinned CLI; the backend Docker build already installs its CLI.

For a fresh checkout, copy `.env.example` to `.env` and `ui/v2.5/.env.example` to `ui/v2.5/.env`, then configure the placeholder values. Actual environment files, local IDE settings, and generated wizard guides are ignored by Git and excluded from Docker build contexts. The previously tracked UI `.env` has been removed from the index; its local values remain on disk.

## What is wired now

The Go process already initializes the PostHog SDK and installs a top-level panic boundary in `cmd/stash/main.go`. Uncaught panics are logged locally and converted into SDK-native exceptions with `posthog.NewDefaultException`; the deferred client close flushes them. The client configuration and lifecycle are in `internal/analytics/posthog.go`. The SDK installation and initialization are therefore in place even though the project started without PostHog.

The UI initializes `posthog-js` before rendering, captures unhandled exceptions and React boundary errors, and tracks SPA navigation through History API pageviews. Authenticated sessions use the same database user ID as backend events. Logout and account changes clear the previous browser identity; initially anonymous visits keep their anonymous ID.

The Vite build uses `@posthog/rollup-plugin` to inject chunk IDs and upload hidden source maps before gzip compression. Successfully uploaded maps are deleted from the output. Vite's separately built legacy polyfill has no PostHog chunk ID, so its unused map is omitted. Local builds read the existing `ui/v2.5/.env` upload variables (`POSTHOG_API_KEY`, `POSTHOG_PROJECT_ID`, `POSTHOG_HOST`); only the `VITE_PUBLIC_*` variables enter browser code. Docker excludes local environment files and mounts the upload key only during each build step.

The pnpm policy fix replaces the Chromecast polyfill's Git source with the identical `webcomponents.js@0.7.24` registry release, scoped to that dependency. Both pnpm 10 and pnpm 11+ allow the PostHog CLI's installation script. The blocking polyfill file was compared byte for byte against the registry release.

Production debug-symbol upload is wired through the traced deployment path:

`.github/workflows/docker-publish.yml` → `docker/build/x86_64/Dockerfile` → GHCR → `docker/production/docker-compose.yml`

The files changed for source-map/debug-symbol upload are:

- `.github/workflows/docker-publish.yml`
- `docker/build/x86_64/Dockerfile`
- `Makefile`
- `.dockerignore`
- `ui/v2.5/package.json`
- `ui/v2.5/pnpm-lock.yaml`
- `ui/v2.5/pnpm-workspace.yaml`
- `ui/v2.5/vite.config.js`
- `ui/v2.5/.env.example`
- `ui/v2.5/src/index.tsx`
- `ui/v2.5/src/components/ErrorBoundary.tsx`
- `ui/v2.5/src/components/MainNavbar.tsx`
- `ui/v2.5/src/hooks/UserContext.tsx`
- `ui/v2.5/src/hooks/UserContext.test.tsx`

The production build command is:

```sh
make build-release-posthog-linux
```

GitHub Actions runs that target and uploads matching Go debug symbols before copying the binary into an image that is deliberately published. `POSTHOG_UPLOAD_REQUIRED` follows the publication planner's `publish` output, so release tags and deliberate test-image publication upload symbols; pull requests, master validation, and manual validation-only builds compile without uploading. The macOS production target is also available as `make build-release-posthog-macos`.

For the frontend, run `pnpm run build` from `ui/v2.5` to build and upload when the upload variables are configured. The build allows an 8 GB Node heap: the initial source-map build exceeded Node's default 4 GB heap. To build without contacting PostHog, use:

```sh
POSTHOG_UPLOAD_REQUIRED=false pnpm run build
```

Serve the resulting frontend through the backend with `make server-start GITHASH=dev STASH_VERSION=dev`; a standalone preview can use `pnpm exec vite preview --port 4173`, but API requests require the backend. With explicit user authorization, 463 application chunk maps were uploaded successfully to project 644503. Modern and legacy entry maps were downloaded from PostHog by their packaged chunk IDs; both contained the exact current `ErrorBoundary.tsx` source and resolved a generated position to its `captureException` call at line 35. No temporary test button or route was added.

Local verification passed: TypeScript, targeted ESLint (four SDK import warnings, no errors), 31 frontend tests (two existing skips), formatting for the edited TypeScript and Vite config, production builds with uploads disabled and enabled, and a frozen lockfile check using CI's pnpm 10.34.4. The running dev server served the updated instrumentation modules. The final upload-enabled build succeeded and PostHog confirmed all 463 application maps were already present. All 668 packaged artifacts, including decompressed gzip assets, were checked: no source maps or personal upload keys remained, and the downloaded entry-map chunk IDs matched the final bundles. Source-map receipt and mapping were verified as described above. The Docker image and end-to-end receipt/symbolication of a real browser error event have not been verified.

Before committing on the updated master base, `go test ./...`, TypeScript, the frontend tests, and all nine publication-policy tests passed again. Ignore rules were checked against local environment files and wizard/IDE artifacts, and commit candidates were scanned for the existing local PostHog tokens and API keys. The Intel packaging tests could not run under the Mac's system Python because it lacks `unittest.TestCase.enterContext` (Python 3.11+ is required).

No local environment-variable values were changed during the UI completion; the existing wizard-provided credentials are reused in the verified GitHub Actions settings above. For a manual analytics check, refresh the local UI, navigate between Scenes and Settings, and check pageviews in PostHog. For identity, sign in and then sign out, checking that subsequent events no longer use the prior account's ID.

## How to verify

Trigger any uncaught error, then open [PostHog Error Tracking](https://us.posthog.com/project/644503/error_tracking). Uploaded symbol sets appear at [Error Tracking configuration](https://us.posthog.com/project/644503/error_tracking/configuration).
