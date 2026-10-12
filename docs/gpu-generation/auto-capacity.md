# Automatic generation capacity

Saved zero requests Auto and remains zero. Backend/device/budget requests activate
on restart; effective limits can change within the active scheduler. Positive
manual limits are preserved. Intel admission is mandatory. Disabling the shared
CPU budget retains ordinary CPU generation and pHash scheduling/batching/threading.
Auto total and threads retain execution-capacity and host/container headroom sizing.
GPU lanes can raise Auto total without raising CPU leaf concurrency.

Manual and Auto share actual source decode/filter/encoder capability checks,
strict GPU execution and final output validation. Cached capacity never skips
these checks. Sprite and preview rendering never falls back to CPU. JPEG header
and video packet validation inspect encoded output; decoded pixels stay on GPU.

The provisional source-surface estimate seeds unmeasured workloads, rather than
permanently capping them. Auto measures owned FFmpeg high-water RSS, including
final wait4 peaks for short children, and optional DRM client memory counters.
Client/device IDs de-duplicate descriptors. Resident counters take precedence over
possibly unbacked requested buffers. RSS plus DRM system allocations and shared
buffers form conservative upper bounds. Missing counters remain unknown, and
unrelated host processes are not charged as render cost. Rolling previews retain
concurrent allocation peaks and the largest per-child peak; completed children
do not accumulate as concurrent cost. Their own packet-check permits remain
owned work, while unrelated budget contention stops throughput learning.

Validated counters independently replace their dimension's estimate with measured
per-lane costs, a 25% allocation reserve and 16MiB minimum. Missing counters retain
that dimension's prior measured cost or provisional reservation; disappearance is
never learned as zero allocation. Sprite decoder lanes share their process's fixed costs:
observed cost per lane can decrease when higher counts are exercised. Separate
preview processes retain per-process costs. Admission still reserves against half
observable current headroom, honors known exhaustion and reduces capacity on
explicit allocation pressure. Available work and actual trials bound exploration;
no fixed 12/15/18 limit or resolution-only capacity rule exists. Missing device
headroom does not establish unlimited or safe driver capacity.

A resident sheet cannot change decoder count after FFmpeg starts. Cold Auto
sprites with multiple rows render representative canonical seeks into disposable
partial sheets through the identical GPU filter/grid/JPEG encoder, then generate
the complete canonical sheet at the fastest tested count. At most six candidates
run within ten seconds and a quarter of the first sample's projected full-sheet
time, including initial sample setup/validation/cleanup. The initial sample must
use at most a fifth of the requested tiles, leaving room to install the forecast
deadline; otherwise calibration is skipped. Candidates compare the same subset.
When more tiles are needed, the best tested count is remeasured on that expanded
subset before comparing a new count; that baseline consumes the trial/time budget.
Faster validated trials grow; rejected larger trials can refine between that
count and the faster tested count. Small/one-row sheets, frame-mode sheets,
a shared total or GPU ceiling of one and a complete-output baseline skip partial
calibration. Partial trials are never published. Cancellation drains children and
removes temporary seek lists and output.

The validated canonical sheet establishes the full-work comparison. A subsequent
generation exercises a pending count on the complete sheet it already needs to
generate; it does not repeat expanded partial baselines. Successful valid output
is published once and compared against the same file/window/grid's full-work
baseline. Faster counts advance the search, slower counts keep the previous best
and can refine between tested counts. The cold calibration budget cannot become
a permanent concurrency ceiling. Contended samples never replace the reference.
Current admission still checks each cost dimension and observable headroom.

A speculative canonical attempt is bounded to 125% of the selected baseline's
render duration, starting after admission. Failure, timeout or invalid JPEG drains
before one strict-GPU retry at the prior validated count. Invalid output is never
published. Parent cancellation aborts and drains without retry or learning. This
retry can add work, so host comparisons must include its complete end-to-end cost.

Cold preview validates one small calibration group, then feeds all remaining
chunks through a rolling worker pool at the resource-informed trial count. Warm
previews use one rolling pool for the complete requested window; each finished
worker starts another chunk while slower siblings continue. Every chunk retains
its canonical seek, duration, audio and concat position; completed chunks are
never rerendered. Packet validation stays inside each worker. The cold pass
retains its exercised count and resource costs without treating its changing
concurrency as a steady throughput reference. The first warm complete pass at
that count establishes the baseline, including concat and final validation.
Later warm passes can trial larger counts against that same file/window baseline.
Throughput is completed tiles/chunks per wall second, including startup.
Improvement must exceed 5% to select a larger count. Source complexity and seek
cost vary, so observations do not establish a universal performance guarantee.

Keys retain runtime/device, codec/profile, physical format/bit depth/dimensions,
filter and operation/grid, and include private-hashed file identity and requested
sampling window. Equal 8K geometry or codec never shares another file's evidence.
Concurrent generations have separate controllers. Identified file evidence stays
in memory only after the complete output validates; failure invalidates it.
Cleanup can publish or invalidate only the base revision its controller copied.
An older overlapping generation cannot replace or erase newer evidence, or
restore evidence after a newer invalidation.
Unknown runtime identity permits adjustment within this generation without
retained learning. Restart requires fresh evidence.

Explicit allocation failures permit one reduced-lane sprite retry after drain.
Generic VAAPI23/24, missing frames, invalid media and correctness errors are never
classified as memory pressure. Speculative calibration and canonical-candidate
failures are logged and discarded. If partial calibration is unavailable, the provisional count feeds the
complete strict render instead of introducing another capability restriction.
That final render retains its own errors and validation. Manual GPU failures
receive no tuning/retry. No error enables CPU rendering.

Local tests cover first-job adaptation beyond oversized estimates, shared-process
costs, slower-trial rejection, identities, manual preservation, missing/exhausted
counters, chunk order and invalid-output/cancellation cleanup. Subprocess fixtures
are not hardware or media-quality proof. Host acceptance must compare the exact
candidate/file/window against that file's working manual baseline under realistic
production-equivalent limits. Count calibration in end-to-end time. Record cold
and warm results, actual lanes/child overlap, memory, all output cells/frames,
timing/audio and zero software generation.


## Independent sprite and preview Auto selections (VEX-71)

Sprite and preview GPU concurrency now tunes independently inside the same
scheduler. Existing positive total and GPU requests remain shared hard ceilings;
they no longer disable sprite/preview Auto learning. Other rendering families
retain their existing Auto/manual behavior. FIFO admission, per-plan limits,
resource reservations, cancellation and strict GPU rendering still apply.

The read-only `generationTuning` response exposes two workload-type selections,
without filenames or overrides. `automaticConcurrency` is nullable and
`measurementState` is `NOT_MEASURED`, `ADAPTING` or `MEASURED`. A number is present
only when a validated complete render supports the currently selected type-wide
ceiling. Seeds, partial trials and pending untested counts never qualify a number.
Unknown allocation counters do not invent costs or justify growth; an isolated
complete-output timing can still qualify its actual count. Contended or failed
outputs cannot establish evidence. Measurement requests do not start media work.
Single-segment previews measure their ordinary rendering command and qualify the
count only after the existing final validation succeeds.

The type-wide ceiling is the maximum of its current in-memory plan selections,
subject to shared ceilings. A later smaller file cannot replace it with a
last-file number. Each job still receives its own potentially smaller grant,
subject to source/plan evidence and shared resource availability. If a larger
untested plan changes that aggregate selection, the displayed number is withheld
until complete-output evidence supports it. Scoped cleanup retains only valid
current evidence; revision checks prevent an older overlapping cleanup from
erasing or replacing newer results. Restart discards observations. These counts
are not universal hardware capability limits and do not repair renderer errors.
