# Frontend error diagnostics

VEX-80 extends the shipped VEX-78 policy. `sanitizeTelemetry` is the production
PostHog `before_send` hook; `createDiagnosticErrorLink` covers Apollo GraphQL and
transport failures. Runtime telemetry remains operator opt-in through both public
project-token and host build settings. DOM autocapture, replay, console capture,
performance collection and remote loading remain disabled. Existing fixed-category
history pageviews remain enabled. No new browsing or media collection is enabled.

## Diagnostic preservation

Exception properties retain unfamiliar technical prose, error types, source
property identifiers, cause/member IDs, handled/synthetic flags, severity,
module/thread metadata, sanitized technical breadcrumbs, browser/OS/version,
language/webview/device category, navigation correlation and SDK configuration,
queue/time/limiter metadata. Novel technical properties no longer require a
registration list. Top-level diagnostic fields use the same field-sensitive
private-content rules as nested context. Structured technical `data`, `args`, `input`, `content` and
`variables` can retain numeric/boolean/object metadata; opaque string leaves or
string arrays in those containers are private. Producers must never pass an actual
request/variables object merely because it is structured.

Application namespace/version/build/revision come from the embedded build. The
SDK release ID comes from the CLI/plugin-injected `_posthogReleaseId`, bounded to
512 characters and required to survive targeted string redaction unchanged.
Caller release IDs cannot override it. The installed plugin injection is tested
with the real parser and production hook. App build/revision is not a substitute
for `$release_id`. Missing injection remains visible as a missing release ID.

Same-origin hashed UI bundle filenames normalize to `/assets/`, retaining line,
column, source function and UUID chunk ID for server-side symbolication. Function
identifiers support Unicode, private methods and parser aliases; long identifiers
are capped at 512 characters. Foreign/plugin/media filenames, raw source lines,
locals and frame variables remain excluded because they can expose private paths,
code or user content. Reverse-proxy prefixes and URL credentials/query/fragment
never become stack filenames. The client does not itself symbolicate frames.

All fifty useful exceptions supplied by the pinned SDK can survive, in source
order. Manual malformed/empty entries do not consume the fifty-exception limit;
missing/nonstring messages retain useful type/mechanism/stack metadata and the
existing `Non-string error message [redacted]` fallback. Each entry retains the
first fifty accepted bundle frames. The SDK has earlier limits that this hook
cannot undo; the matrix below distinguishes them.

## Private content and bounded work

Strings retain unknown diagnostics while redacting native JSON input previews
(parser position/line/column survives), known source call sites that append HTTP
response bodies, labelled response/body/payload/input/content tails, JSON/HTML
bodies, PEM private keys, cookie headers, Bearer/Basic credentials, credential
assignments, JWT/common opaque-token prefixes, URLs, private paths, media filenames,
email addresses and explicit personal/content assignments. Percent decoding runs
at most twice. Quoted assignments preserve following prose; unquoted credential
assignments remove the remainder of their line because credentials can contain
spaces and delimiters. Unrecognized unlabelled personal text or token formats are
not reliably classifiable and may survive; use static diagnostic prose and
explicit private fields at producer boundaries.

Named credential/private fields (including `$`-prefixed and camel/dot/hyphen names)
are replaced without serializing their children. This includes keys, secrets,
auth/cookies, usernames/email/title, filenames/paths/URLs, headers, raw bodies,
requests/responses, payloads and attachments. Raw SDK URL/referrer/campaign/person
properties, raw user-agent text, timezone city, custom API host and redundant device
identity are excluded. Exact viewport/screen sizes become hundred-pixel buckets,
retaining layout context while reducing fingerprint precision. Fixed-category
pageviews and authenticated numeric-ID/role identification stay minimal.

Context and exception metadata have independent bounded inspection scopes. Each
scope has a 2,000-value and 128KiB technical-text budget, plus 100,000 object-key enumeration
steps. Optional context cannot consume cause/frame inspection capacity.
Cause messages also retain their separate per-message capacity. Containers allow
depth eight,
100 accepted object fields or 100 array items. Entries inspect at most 10,000
items/keys per container, with 100,000 object-key enumeration steps per scope.
Messages inspect at most 65,536 characters and retain 8,192 plus a marker.
Private patterns are removed before output truncation. Private-key blocks and
incomplete tails do not leak a cut key prefix. Unsupported types, non-finite
numbers, accessors and malformed keys are omitted. Getters are never intentionally
evaluated; cycles have a specific omission marker. These bounds protect the UI
thread and envelope size, rather than narrowing errors to a fixed vocabulary.

`telemetry_diagnostics` reports redacted/truncated/omitted values, cycles, skipped
accessors, total and per-scope object-key inspections, supplied/retained exception
counts and explicit local/pinned upstream limits. Array items are bounded separately; inspection counts
cover object-key enumeration. Counts describe the input seen by this hook, not
upstream losses or exact omitted bytes. Truncation counts are signals per bounded
container/string; shared-budget exhaustion can increment more than once. Invalid
entries and private frames can be omitted before the retained-entry limit.

## API coordination and delivery health

A GraphQL error is suppressed individually only when
`extensions.telemetry_captured === true` and `extensions.telemetry_event_id` is a
UUID. The backend sets these only after successful enqueue. Mixed responses still
capture every unmarked error, and transport failures remain eligible. Backend
capture owns detailed safe diagnostics; frontend fallback emits a generic operation
failure with the shipped error-code/schema identity, not the raw server message.
Operation identity must match the generated application documents. Caller names,
aliases, query text, variables, request/result objects and response bodies are not
exported by the Apollo link. Within the Apollo link, shared error objects are deduplicated without changing
response/error propagation; a synchronous SDK failure cannot break a request.

Successful enqueue is not delivery. `telemetryDeliveryHealth()` exposes local
rejected-event, synchronous API capture-failure and SDK HTTP-failure-attempt counts,
plus last HTTP status. Later exception/warning events include that snapshot. The
SDK `on_request_error` callback observes HTTP status >=400, not status-zero network
failures, final retry exhaustion or successful ingestion; no receipt is inferred.
The SDK's actual `$$client_ingestion_warning` is retained with private triggering
page details redacted. Its message reports the SDK's drop tally at warning time,
not every subsequent drop. These observations never recursively capture errors.
SDK retries, quota pauses and consent/browser suppression remain SDK-owned.

## Audit coverage matrix

Classification: **Implemented** changes this candidate; **Retained with reason**
keeps an existing safety/collection boundary; **SDK-inherent** precedes/bypasses the
hook; **Live-unverified** requires live evidence that was not requested here.
Numbers map to the 62-item frontend audit, rather than implying all exclusions are
bugs. Tests use PostHog 1.435.8, core 1.55.3 and plugin-utils 2.0.0.

| # | Boundary / disposition | Classification |
|---|---|---|
| 1 | Both runtime settings required; avoids accidental opt-in. | Retained with reason |
| 2 | Global errors/rejections and ErrorBoundary remain; Apollo adds caught API failures. | Implemented |
| 3 | Console error/log collection off; console can include arbitrary user/body data and duplicates. | Retained with reason |
| 4 | History pageviews remain fixed screen categories; no item/query text. | Retained with reason |
| 5 | Pageleave off; unrelated browsing behavior adds no failure diagnostics. | Retained with reason |
| 6 | DOM/autocapture/replay off; private media/user text. | Retained with reason |
| 7 | Performance/web-vitals/network metrics collection off; no need to broaden traffic/body collection. Existing exception technical metrics survive. | Retained with reason |
| 8 | Surveys/tours/conversations/remote code/flags/toolbar off; keeps private UI free of remote behavior and marketing collection. | Retained with reason |
| 9 | Three app event types plus SDK ingestion warning accepted; unrelated custom/DOM events excluded. | Implemented |
| 10 | Missing client/settings, DNT, consent and bot filters precede hook. | SDK-inherent |
| 11 | 10/s, burst100 global limiter and automatic exception-type limiter remain flood protection; real warning survives. Exact upstream drop count unavailable to hook. | Implemented / SDK-inherent |
| 12 | SDK suppression, extension/injected-script and SDK self-error filters remain. Sanitized technical drop breadcrumbs survive on later errors. | SDK-inherent / Implemented |
| 13 | SDK extracts Error name/message/stack/cause; arbitrary own Error properties and object summaries are SDK-owned. Put technical context in capture properties. | SDK-inherent |
| 14 | SDK causes50/aggregate members1000/prototype100/wrapper4/cycle and truthy-cause limits remain; local cause cap now50. | Implemented / SDK-inherent |
| 15 | SDK scans1000 stack lines, skips >1024-character lines, reverses frames and caps50 before hook. | SDK-inherent |
| 16 | SDK uuid/event/timestamp envelope retained; unrelated top-level attachments/person objects excluded. | Retained with reason |
| 17 | Public token/library/version retained; delivery and SDK attribution. | Retained with reason |
| 18 | Build-owned app namespace/version/build retained; fallback development remains. | Retained with reason |
| 19 | Actual CLI/plugin release global retained as `$release_id`; caller cannot substitute revision. | Implemented |
| 20 | Authenticated numeric/anonymous UUID identities retained; no names/email. | Retained with reason |
| 21 | Session/window UUIDs retained; SDK 30min idle/24h max behavior unchanged. | Retained with reason / SDK-inherent |
| 22 | Identification requires positive numeric matching user ID and admin/viewer role. | Retained with reason |
| 23 | Person profiles only identified roles; existing logout/401/switch reset unchanged. | Retained with reason |
| 24 | Useful metadata-only entries accepted; accepted exception cap increased10 to50. | Implemented |
| 25 | Valid technical error-type identifiers retained; malformed values use Error. | Retained with reason |
| 26 | Unknown messages retained, six SDK severity levels preserved. | Retained with reason |
| 27 | SDK handled/synthetic/type/source/cause IDs retained with existing type/numeric validation. No new producer of other mechanism values found. | Retained with reason |
| 28 | Safe module/thread metadata now retained. | Implemented |
| 29 | Only shipped same-origin hashed bundles become filenames; private plugin/media/foreign paths excluded. | Retained with reason |
| 30 | Normalize own bundle to /assets; removes private installation/query/credential details. | Retained with reason |
| 31 | Line/column/UUID chunk retained; web platform and own-bundle in_app=true. | Retained with reason |
| 32 | Broader technical function syntax retained, with bounded identifier length. | Implemented |
| 33 | First50 accepted bundle frames retained; source lines/locals/variables excluded for private content. | Retained with reason |
| 34 | Up to two percent-decoding passes before masking. | Retained with reason |
| 35 | JSON preview hidden; complete parser position/line/column retained. | Implemented |
| 36 | Known HTTP response appenders redact private body, retain operation/status. | Retained with reason |
| 37 | Sprite/VTT/image/status response tails hide private bodies; exact status survives. | Retained with reason |
| 38 | Labelled raw body/content/input tails hidden. | Retained with reason |
| 39 | JSON/HTML payload tails hidden. | Retained with reason |
| 40 | URLs redacted as a whole because host/path/query may all be private. Bundle filenames have their separate safe normalization. | Retained with reason |
| 41 | Whole/incomplete PEM private keys redacted before truncation. | Retained with reason |
| 42 | Cookie/Bearer/Basic credentials hidden. | Retained with reason |
| 43 | Credential assignment patterns remain conservative; unquoted tail and token-like key false positives are documented limits. | Retained with reason |
| 44 | Common JWT/token shapes hidden; unknown shapes require producer labels. | Retained with reason |
| 45 | Explicit personal/media assignments hidden; source property identifiers preserved. | Retained with reason |
| 46 | Paths/media filenames/email hidden, Unicode included. | Retained with reason |
| 47 | 8KiB messages with 64KiB inspection window and observable truncation, replacing2KiB. SDK noTruncate for exceptions remains. | Implemented |
| 48 | Novel technical capture properties retained without eight-field registration list. | Implemented |
| 49 | JSON-like types retained; getters/cycles/malformed/unsupported values safely omitted with counters. | Implemented |
| 50 | Explicit private fields hidden; structured technical data retained in previously blanket-redacted containers. `$` prefixes recognized. | Implemented |
| 51 | Independently bounded context/exception inspection budgets, larger depth/accepted-field limits and visible omission signals. | Implemented |
| 52 | Useful React component stack retained with targeted string redaction; no demonstrated private dynamic name producer. Local UI error rendering unchanged. | Retained with reason |
| 53 | Browser/OS/webview/language/device category/offset retained; raw UA/timezone city excluded; dimensions bucketed to reduce fingerprint precision. | Implemented |
| 54 | Navigation correlation/technical timing retained when supplied, URLs masked; no additional navigation collection enabled. | Implemented |
| 55 | SDK technical exception steps retained and sanitized; upstream32KiB buffer eviction/oversize rules remain. | Implemented / SDK-inherent |
| 56 | Safe SDK config/channel/time/queue/limiter metadata retained; custom API host and redundant device identity excluded. | Implemented |
| 57 | Sentry bridge is not configured; duplicate exception messages/URLs and unconfigured Sentry person payloads excluded. No ordinary producer claimed fixed. | Retained with reason |
| 58 | SDK local URL/referrer/session-entry persistence precedes hook even with campaign flags off; those fields excluded from exported envelope. | SDK-inherent / Retained with reason |
| 59 | Persistence/auth defaults unchanged; GeoIP disabled does not hide transport IP. | SDK-inherent |
| 60 | SDK batching/encoding/retry/quota behavior unchanged; HTTP failure attempts observed locally, warning retained. Status0/retry exhaustion/ingestion confirmation unavailable through public callback. | Implemented / SDK-inherent |
| 61 | Separate logs/metrics APIs bypass hook; no app producer configured, none enabled. | SDK-inherent |
| 62 | Historical issue title/fingerprint may remain; no current payload, delivery or production symbolication verification. | Live-unverified |

## Offline verification

Run from `ui/v2.5`:

```sh
pnpm exec graphql-codegen --config codegen.ts
pnpm exec vitest run --maxWorkers=1
pnpm run check
node --test scripts/posthog-build-config.test.mjs
```

Tests cover real Apollo links, marker validity/mixed errors/dedup, private names,
original response propagation, actual PostHog parser plus installed injection,
technical breadcrumbs/context, credential/private-content fixtures, mocked SDK
HTTP failure and flood warning, malformed/accessor/cyclic/bounded inputs. The
parser test intercepts the post-hook request at transport; the dispatch test
installs a fake fetch before SDK import. No actual telemetry leaves these tests.
No new rendered UI behavior or headless browser tooling is required by this change.

The offline evidence establishes local payload/dispatch behavior only. It does not
establish live delivery, backend enqueue correctness, source-map upload completeness,
server symbolication, a changed issue title or the original PythonTools install
cause. Live PostHog detail retrieval was cancelled and not retried. Backend coverage
and independent combined review belong to the coordinating VEX-80 workflow.
