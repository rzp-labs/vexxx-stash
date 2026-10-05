# PostHog analytics and error tracking

Telemetry is disabled unless both the relevant project token and ingestion host
are configured. Missing settings do not prevent development or production startup.
Supplying both settings is the installation operator's explicit opt-in. Inform
users before enabling telemetry for a shared installation; browser Do Not Track
and PostHog's persisted opt-out are respected. No production configuration change
or deployment is included in this branch.

Server settings are `POSTHOG_PROJECT_TOKEN` and `POSTHOG_HOST`. The frontend uses
`VITE_PUBLIC_POSTHOG_PROJECT_TOKEN` and `VITE_PUBLIC_POSTHOG_HOST`, embedded during
image compilation. The existing repository configuration targets the operator's
project 644503 at `https://us.posthog.com`, with browser ingestion through
`https://us.i.posthog.com`; there is no hardcoded project token in source.
Actual environment files, credentials and wizard artifacts are ignored and
excluded from Docker contexts. Do not copy placeholder values into a running
installation without configuring them.

## Captured data

Backend events record successful object creation and metadata task starts using
fixed event names, a database user ID, and build version/revision. No mutation
inputs, filenames, media URLs, content or request headers are attached. Anonymous
and legacy single-user actions use `server` without creating a person profile.
Login identification exports only the database ID and role, never the username.
IDs are scoped to the installation; use separate PostHog projects for unrelated
installations to avoid cross-installation ID collisions.

Only fixed starting/stopping records use the isolated OTLP exporter. Application
logs stay local. The main-process panic boundary redacts the panic value and
preserves SDK-native stack frames and binary debug images for symbolication;
it does not promise to recover panics in arbitrary goroutines. Client shutdown is
bounded to five seconds.

The UI records page views by fixed screen category and captures unhandled errors
and React boundary errors. A positive payload allowlist removes URLs, referrers,
query strings, DOM data, arbitrary person properties, raw error messages,
React component text and stack source context. Error payloads retain standard
error types, generated asset filenames, safe function names, line/column numbers,
chunk IDs, revision, and pseudonymous user/session IDs. Media and plugin frames
are excluded. DOM autocapture, replay, console capture, performance capture,
remote feature configuration, surveys and remote script loading are disabled.
The error parser is bundled locally. Logout/account changes reset browser identity.

## Symbols and releases

The browser exception release is `vexxx-ui`; its release version and uploaded map
release version use the same build revision. The pinned Rollup uploader injects
chunk IDs and uploads hidden maps before output compression. Uploaded maps are
deleted and uninstrumented legacy-polyfill maps are omitted.

`POSTHOG_UPLOAD_REQUIRED=false` explicitly disables source-map uploads even when
credentials exist. Use this for local validation:

```sh
POSTHOG_UPLOAD_REQUIRED=false pnpm run build
```

Normal builds retain a 4 GiB Node heap. All builds use one minifier worker to
bound aggregate memory. Upload-enabled map builds use the locally verified
6 GiB allowance; offline map generation exceeded
4 GiB. The launcher and Vite share the exact upload-eligibility predicate.

Normal PR/master validation neither publishes images nor exposes the PostHog
personal upload key. Docker source/symbol uploads require deliberate publication
eligibility AND the planner output; the credential expression independently
rejects PR and normal master events. BuildKit mounts the key only for those build
steps. Package-write permissions remain in the separately guarded publisher.
The backend CLI is pinned to 0.16.2, matching the frontend lockfile.

The existing `build-release-posthog-linux` and `build-release-posthog-macos`
targets deliberately build and upload symbols; do not run them for local tests.
Go binaries retain DWARF and native build IDs for matching debug images. Source
maps, sources and symbols may contain private repository code: uploads are
reserved for explicitly authorized release publication, never ordinary tests.

## Verification

Local regression tests verify anonymous event validity, username exclusion,
no-configuration development startup, browser payload redaction and useful chunk
positions, and the workflow's credential/publication boundaries. Frontend types,
unit tests and production compilation, plus backend generation/tests/build and
packaging/policy tests are the validation gates. No test sends real telemetry or
uploads source maps, secrets, or media. End-to-end remote receipt/symbolication
and production enablement require a later authorized verification/publication.
