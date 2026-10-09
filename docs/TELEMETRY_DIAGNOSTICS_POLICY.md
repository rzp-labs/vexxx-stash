# Backend diagnostic policy (VEX-80)

Telemetry is enabled by the bundled release destination or a complete runtime
project override. A telemetry initialization or delivery failure must not stop
application startup, jobs or API responses. This policy covers structured
failures; application log streams and request/response bodies are not exported.

Useful unfamiliar diagnostics are retained. The shared `pkg/diagnostics` policy
removes credential assignments, authorization/cookie records, recognizable key
and token formats, private-key blocks, URLs, email identities, private paths and
media filenames. Callers provide known private values for exact replacement;
GraphQL argument strings are used as local redaction targets, never raw log values. Generation provides
known paths and scene titles. Unknown unlabelled content cannot reliably be
classified as a secret or media identity. There is no blanket allowlist of
messages; this remaining limitation requires a concrete producer/fixture before
further targeted rules can be justified.

Operation/cause summaries preserve their beginning and end. Process output keeps
its useful final records separately. Python job-status output retains its 4096
byte compatibility bound. Package events retain up to 64 module causes, compact
per-module summaries, and 64 KiB total output, with 4096 bytes per module. Generic
and generation reports keep separate causes and output. The shared sanitizer
works on complete head/tail records within 256 KiB, with explicit omitted-byte
counts. Oversized or partial credential/path records are omitted; a partial PEM
block never becomes unlabelled key material. UTF-8 boundaries are preserved.

Property serialization has a generous 128 KiB string budget, 4096 visited nodes,
256 entries per collection, and depth 16. Identity, concise causes and aggregate
counts have priority over verbose detail. Error trees have depth 128 and 64
reported leaf failures; application batch continuation is unaffected. Omitted
bytes, records, candidates and detailed failures are reported. These are resource
bounds, not promises of retaining arbitrarily large private or diagnostic data.

A saved native stack uses the SDK's actual failure-site frames and image IDs.
Generic errors without a saved stack label their stack as the capture boundary.
Panic recovery captures the still-unwinding panic stack. Source roots and local
binary paths are rewritten; function/line/inline/native address/debug-ID metadata
remains. Unsupported platforms retain the SDK's plain Go fallback. Source upload
and live server-side symbolication remain release operations, not local test work.

Generation/package captures mark accepted failure identities in the job context.
The ordinary job boundary captures remaining real failures. Concurrent observers
reserve identities; a rejected enqueue releases the reservation. Pure
cancellation and cancellation-killed children are omitted, while a real failure
joined with cancellation is retained. A thumbnail canceled before starting does
no work. Success, partial package install refresh, continued module/package
batches, permit ownership and retry behavior are preserved.

The GraphQL diagnostic view strips response-path decoration. Before resolver
execution, framework validation also redacts exact caller-chosen operation,
fragment and variable names, aliases, unknown schema names and literal values
collected locally from bounded query tokens. Public schema vocabulary, validation
rules and parse/coercion causes remain useful. Invalid enum variable values are
removed at the pinned coercion formatter; malformed JSON body echoes are removed
at the pinned transport formatter. An explicit diagnostic
input-limit message replaces request details when bounded analysis cannot complete.
These changes affect capture only; client-visible GraphQL responses stay unchanged.

Local GraphQL error and recovery logs also apply the targeted privacy policy.
They retain schema field, operation type, unfamiliar causes and bounded context;
response aliases and known private argument values are removed. Debug argument
records retain schema argument names with every value redacted, including nested
inputs. Debug logging never serializes password-mutation values.
The native panic stack remains local. Client response behavior is unchanged.

GraphQL captures the original safe diagnostic before client-facing sanitization.
Context is the schema field and operation type, never an alias, user-supplied
operation name, query, variable object, request or response. Recovery returns a
safe internal-error response and its accepted event ID. `telemetry_captured=true`
plus a valid `telemetry_event_id` means the SDK accepted the enqueue; it does not
mean the receiver delivered or ingested the event. The frontend suppresses only
that marked error, retaining mixed unmarked and transport failures.

Delivery acceptance, success, SDK discard, enqueue rejection, SDK warnings and
log export failure have cumulative counters. Subsequent events carry them; a
local warning is rate limited to once per category per minute, with a final
local shutdown summary. Delivery callbacks never recursively enqueue. PostHog and OTEL retain their bounded queues, retry and
shutdown behavior. OTEL uses explicit safe service/version/build resources;
ambient resource environment values are not exported. Its two lifecycle records
are separate from application logs. SDK queue overwrites and receiver-side
processing have no per-record acknowledgement API; they remain limitations.

## Audit coverage

`Implemented` identifies changes in this candidate. `Retained` records a
justified existing behavior. `SDK-inherent` identifies behavior owned by the
pinned SDK. `Live-unverified` requires release/runtime evidence unavailable to
these local offline tests. Rows can include more than one classification.

| Audit item | Disposition | Concrete coverage or reason |
| --- | --- | --- |
| 1 Application logs | Retained | Structured captures cover failures without exporting a wholesale log stream. |
| 2 Capture gaps | Implemented / Retained | Ordinary failed jobs and GraphQL/recovery now capture; existing generation/legacy failure sites remain. Nil-returning arbitrary logs are not treated as exceptions. |
| 3 Destination/opt-out | Retained | Keep bundled/default telemetry enabled; no opt-out UI or disable switch added. |
| 4 Cancellation | Implemented | Preserve genuine joined failures; omit pure cancellation and killed children; pre-canceled thumbnail performs no work. |
| 5 Deduplication | Implemented | Job-scoped accepted identities and concurrent reservations; rejected enqueue can retry later. Recovery event marker avoids presenter duplicate. |
| 6 Server identity | Retained | `server` identifies process events, without a private host identity. |
| 7 Job correlation | Retained / Implemented | Opaque UUID is shared by specialized and ordinary failure boundaries. |
| 8 Person profiles | Retained | Failure events do not create person profiles. |
| 9 Authenticated activity | Retained | Existing numeric user/activity identifiers and login role; no media/user content added. |
| 10 Request context/GeoIP | Retained | No raw request context; server SDK GeoIP disabled by default. |
| 11 Common property defense | Implemented | BeforeSend applies shared redaction and generous budgets, preserving unknown safe properties. |
| 12 Generation wrapper loss | Implemented | Operation/cause is separate from stderr; joins retain wrapper prefixes/suffixes. |
| 13 No-output failures | Retained / Implemented | Explicit no-output cause survives; command arguments are omitted. |
| 14 Filesystem wrapper loss | Implemented | Typed paths provide exact redaction; unfamiliar OS causes and wrappers remain. |
| 15 Stderr input bound | Implemented / Retained | Shared redaction has a 256 KiB record bound with byte counts; existing process stderr capture and metadata prefilter still visit captured stderr. |
| 16 Metadata line filtering | Implemented | Drop content-bearing metadata sections/tags and input/output locations; retain version/build, technical stream and driver records. Count omitted records. |
| 17 Known private values | Implemented | Paths, filenames and scene titles; GraphQL argument strings remain local redaction inputs. |
| 18 Generation credentials | Implemented | Same stronger assignment/header/key/token policy as Python and panic reports. |
| 19 URLs | Retained / Implemented | Broad supported URL schemes are removed by one shared policy. |
| 20 Quoted paths | Implemented | Shared policy; public render-device paths retained even when quoted. |
| 21 Unquoted paths | Retained / Implemented | Ambiguous remainder is omitted with known OS suffix retained; typed/known exact paths preserve arbitrary following causes. |
| 22 Render-device context | Implemented | `/dev/dri/renderD<number>` retained across all reports; other locations redacted. |
| 23 Media filename matching | Implemented / Retained | Ordinary ASCII matching no longer greedily removes preceding words; Unicode/space ambiguity remains conservative. Known values and quotes resolve complete names. |
| 24 Email identities | Retained | Shared targeted identity removal. |
| 25 Generation byte/UTF-8 bounds | Implemented | Independent cause/output bounds; omitted bytes explicit; complete UTF-8 retained. |
| 26 Backend identifiers | Implemented | Future unfamiliar safe backend labels retained through targeted redaction. |
| 27 Workload identifiers | Implemented / Retained | Stable producer workload names, no private descriptions; unfamiliar safe future names retained. |
| 28 Failure stage | Implemented | Future stages retained; existing decode/filter/encode inference remains useful. |
| 29 Generation properties | Retained / Implemented | Existing budget/admission/backend/exit/status context plus unrestricted safe codec text. |
| 30 Source/device/driver context | Implemented | Profile, pixel/color/rate/aspect/rotation/matrix/bit depth, safe source errors/fingerprint, bounded candidates, device/filter/probe stages/timeout, FFmpeg version/build. Raw input/probe arguments excluded because they carry private locations. |
| 31 Generation stack | Implemented | Actual SDK native snapshot saved at command failure; no invented observer origin. Errors without snapshot keep no fabricated origin stack. |
| 32 Unknown panic text | Implemented | Preserve targeted-redacted unfamiliar string/error causes; structured panic values get field-aware redaction. |
| 33 Runtime panic messages | Implemented | Useful runtime messages retained without a sentence allowlist. |
| 34 Panic filesystem messages | Implemented | Preserve wrapper, operation and arbitrary cause using exact path removal. |
| 35 Source paths | Implemented | Repository-relative paths retained idempotently, including nested `pkg/pkg`. |
| 36 Binary paths | Retained | Standard release paths preserved; private local build roots rewritten. |
| 37 Native SDK fields | Implemented / SDK-inherent | Saved actual stack/image metadata retained; copies avoid shared mutable stack races. Platform fallback remains SDK-owned. |
| 38 Python credential assignments | Retained / Implemented | Strong rule shared across producers; ambiguous unquoted credential record omitted. |
| 39 Python headers | Retained / Implemented | Complete cookie/authorization records removed by shared policy. |
| 40 Token formats | Retained / Implemented | Recognizable prefixed/JWT/AWS token forms shared across producers. |
| 41 PEM blocks | Retained / Implemented | Complete and unterminated blocks removed, including oversized head/tail handling. |
| 42 Python URLs/known locations | Retained | Plain module/requirement names remain useful; locations are redaction inputs. |
| 43 Python path causes | Implemented / Retained | Typed paths retain unfamiliar following causes; ambiguous plain records remain conservative. |
| 44 Unicode filenames | Retained / Implemented | Combining marks/symbol fixtures removed; preceding ordinary ASCII diagnostics retained. |
| 45 Python field limits | Retained / Implemented | Job output stays 4096 bytes; producer retains omitted-byte count; telemetry summary/output separate. |
| 46 Package job summaries | Retained | R4 preserves requirements wrappers and every module cause separately from output, with As/Is unchanged. |
| 47 Package detail cap | Implemented | 64 failures and compact per-module summaries replace old eight-record cap; total count/omissions explicit. |
| 48 Headline displacement | Implemented | Process output is a separate property; compact identities prioritized under event budget. |
| 49 Package telemetry wrappers | Implemented | Ordinary operation/requirements context around command/joins survives. |
| 50 Package context | Retained / Implemented | Operation/plugin/stage/exit/partial-install/job UUID and aggregate causes retained. |
| 51 Package stack images | Implemented | Python command snapshots use actual native SDK frames/images; saved Go stack fallback remains available. |
| 52 GraphQL diagnostics | Implemented / Retained | Original diagnostic captured on server; existing client-facing internal sanitization/codes retained. |
| 53 GraphQL recovery | Implemented | Exactly one panic capture; safe internal response; accepted UUID marks browser suppression. |
| 54 GraphQL local args logs | Implemented | Local error/recovery diagnostics use targeted redaction and schema context. Debug records retain argument names with values redacted, including nested inputs. No raw API request export. |
| 55 Job failures/history | Implemented / Retained | Ordinary errors capture; panic status includes cause. Existing ten-item UI history and throttle unchanged; durable telemetry depends on delivery. |
| 56 Local application logging | Retained | Existing logger/cache/feed/rotation behavior unchanged; no wholesale export. |
| 57 Upstream formatter loss | Implemented / Retained | Capture typed underlying causes/output at boundaries; local upstream formatting/stdout behavior unchanged. No raw media stdout export. |
| 58 exec.Output stderr | SDK-inherent / Retained | Go's existing prefix/suffix stderr capture retained; custom generation retains its captured stderr. |
| 59 Release and SDK metadata | Retained / Implemented | Release provenance preserved; common hook leaves reserved typed SDK stack/image fields intact. |
| 60 SDK delivery loss | Implemented / SDK-inherent | Local cumulative acceptance/success/discard/enqueue/warning counters; no recursive exception storm. Queue/retries remain SDK-owned. |
| 61 SDK reserved fields | SDK-inherent / Implemented | Typed reserved serialization and validation remain; common defense handles application properties; invalid/oversized SDK rejection is counted. |
| 62 OTEL resource environment | Implemented | Explicit safe service/version/build resources; arbitrary ambient values excluded. |
| 63 OTEL delivery/limits | Implemented / SDK-inherent | Export failures counted locally; queue/attribute/retry/shutdown limits remain; two lifecycle records do not justify an application-log bridge. |
| 64 Release symbolication | Retained / Live-unverified | Existing symbol/source build workflow unchanged. Wire metadata verified offline; live upload/rendering waits for parent's coordinated build and runtime return. |
