# Frontend error diagnostics

The UI uses the bundled `posthog-js/no-external` SDK and exception parser. Runtime
telemetry remains operator opt-in through the existing public project token and
host build settings. This policy changes error sanitization only; pageview
categories, DOM autocapture, replay, console capture and remote loading settings
are unchanged.

## Retained diagnostics

`sanitizeTelemetry` is the production `before_send` hook. Exception reports keep
unfamiliar technical messages and source property identifiers, including
`Cannot read properties of undefined (reading 'decodeFrame')`, without a fixed
error vocabulary. Syntactically valid custom error types are retained. The SDK's
cause/member relationships, exception IDs, handled/synthetic flags and severity
remain available. Messages are capped at 2,048 characters plus a truncation marker.
The first ten exception entries and first fifty frames per entry are processed.

Additional properties passed to `captureException(error, properties)` can include
`operation`, `stage`, `component`, `code`, `status`, `retry_count`, `context`, and
`diagnostic_context`. Context accepts nested plain objects, arrays, strings,
finite numbers, booleans and null. New technical keys inside these containers do
not need to be registered. Objects retain at most thirty fields, arrays twenty
items, and nesting four container levels; a shared 200-value traversal budget per
diagnostic property bounds larger/circular objects. Unsupported object types and
non-finite numbers are omitted. `ErrorBoundary` supplies `react.render`, its
component name, and the React component stack in this context.

App namespace, version, build and revision always come from the embedded build.
Caller-supplied release fields cannot override them. Stack frames retain same-origin
hashed UI bundle filenames normalized to `/assets/`, line/column, source function
identifiers (or `?`), and UUID chunk IDs. These are the inputs for server-side source
map symbolication; the client does not perform or verify symbolication itself.

## Redacted data and reasons

Error/context strings redact:

- HTTP response tails from existing call sites and labelled response/body,
  payload, input or content tails, JSON object payloads, and HTML document bodies:
  these can contain arbitrary server/user content. Native JSON input previews are
  removed; a fully matched parser position/column can remain.
- Bearer/Basic credentials, token/password/secret/API-key/auth/cookie assignments,
  JWT-shaped values and common private-key/token prefixes: these are credentials.
  Cookie/Set-Cookie header values are removed through the end of their line,
  including every semicolon-separated cookie. Quoted credential assignments stop
  at the matching quote; unquoted assignments remove the complete remaining line
  because spaces, commas and semicolons can be part of the credential. Diagnostic
  prose before the assignment and on subsequent lines remains available.
- PEM private-key blocks (including RSA, EC, OpenSSH and encrypted variants) are
  removed before message truncation. An incomplete block is removed through the
  end of the message so a partial key cannot survive.
- Entire URLs (including credentials, private host/path, query and fragment),
  Unix/Windows/UNC paths, media filenames and email addresses: these can identify
  private installations, media or people. Percent-encoded variants are decoded for
  up to two passes before applying the same rules. Quoted private path/media
  strings are removed as a whole, including spaces. Unicode media filenames and
  their attached path segments are removed together; package identifiers such as
  `python-tools@2.5.1` and `av==12.0.0` remain intact.
- Explicit username/email/title/filename/path/URL assignments: their values are
  personal or content identifiers, rather than executable source property names.

Nested diagnostic fields whose camel/snake/dot/hyphen-separated names identify
credentials or private content (such as `privateKey`, `accessToken`, `response_body`, `headers`,
`media_path`, `title`, `input`, `data`, or `variables`) become
`[private content redacted]` without inspecting or serializing their children.
Technical names such as `securityMode` and `responseStatus` remain intact.

The SDK envelope still excludes arbitrary event properties, person attributes,
URLs/referrers/campaign values, attachments, frame variables and source-code
context lines. Foreign/plugin/media stack frames are omitted, and the reverse
proxy prefix is removed from accepted UI bundle names. The public ingestion token
is retained because the SDK needs it to deliver events; it is not an upload key.

This is targeted redaction, not a general personal-data classifier. Producers must
use static diagnostic prose and technical context: do not interpolate arbitrary
user text, response bodies, names, unlabelled opaque credentials or content under
an innocuous technical key. Use the explicit private fields above for data-bearing
leaves. Identifiers with no data-bearing syntax are intentionally preserved;
privacy cannot be inferred from spelling alone. Oversized free text is truncated,
not replaced with a generic error. Unknown token formats and arbitrary unlabelled
personal content cannot be guaranteed to be recognized by string patterns.

## Offline regression verification

From `ui/v2.5`, run:

```sh
pnpm exec graphql-codegen --config codegen.ts
pnpm exec vitest run src/core/telemetry-redaction.test.ts src/core/telemetry.test.ts src/core/telemetry-sdk.test.ts src/components/ErrorBoundary.test.tsx
pnpm run check
```

The GraphQL generation uses local schema/operation files and writes the ignored
frontend generated file. It makes no backend or schema changes. Redactor and
whole-event tests cover unfamiliar TypeErrors/custom errors, nested context,
credential/body/path fixtures, malformed entries and bounded inputs. The SDK test
uses installed PostHog 1.435.8 with the production `before_send` hook, actual
`captureException` parsing, a TypeError plus cause, build metadata and chunk IDs.
It intercepts `_send_retriable_request` with a mock and asserts the sanitized
payload handed to transport. Fetch/XHR are guarded and asserted unused. No
telemetry is uploaded by this test.

This establishes local payload behavior for
[VEX-78](https://linear.app/rzp-labs/issue/VEX-78/preserve-unfamiliar-frontend-errors-and-safe-context-through-telemetry).
It does not establish current production payloads, event delivery, server-side
symbolication, or a changed PostHog issue title. An existing fingerprint can retain
an older issue title. Live event detail retrieval was cancelled and was not retried.
