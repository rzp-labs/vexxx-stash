# Automatic generation capacity

A saved zero requests Auto; it is never rewritten to an effective number.
Backend/device/budget requests still activate on restart. Effective limits can
change within the same scheduler without making a restart pending. The shared
CPU switch still controls ordinary CPU generation and pHash; Intel admission is
mandatory even when that switch is off.

Auto total-process capacity uses Go's available execution capacity (including
Linux affinity/cgroup quotas) and available host/container memory. An explicit
or learned GPU capacity raises an automatic total to accommodate it. Ordinary CPU leaf work keeps its separately detected process capacity, so
a GPU capable of more lanes than the CPU core count does not increase CPU
process concurrency. Auto threads divide available CPU capacity by the current
total-process limit and recalculate when it changes. Explicit positive values
remain requests and are not tuned or overridden by workload estimates.

For GPU rendering, the actual source decode, filter and encode probes are the
shared capability gate for both manual and Auto. A learned capacity never skips
those probes or the existing strict hardware and canonical output checks. Auto
keys observations by runtime/device, source codec/profile/pixel format, physical
input dimensions, rendering filter and output operation/grid. HDR/VR projection
must retain physical input geometry for cost accounting.

The initial trial uses detected memory headroom and estimated source-surface
costs, with a gradual start within that envelope. These estimates reserve half
of observable host/device headroom and account for decoder/filter pools and
extra libplacebo working surfaces. They are planning estimates, not codec
allowlists, driver guarantees or proof that a number of lanes is safe. Missing
memory counters mean unknown headroom; zero available memory is distinct.
Admission counts every decoder lane and is cancellable, FIFO and shared across
sheets and previews. Successful, validated rendering cautiously increases a
workload's trial capacity. Observations are in memory only: restart or a changed
runtime/workload identity starts fresh. An incomplete runtime fingerprint still
permits a detected resource trial, but rendering evidence is not reused across
plans whose runtime cannot be identified.

Explicit allocation/out-of-memory failures reduce Auto capacity. A resident
sprite may retry once with fewer lanes, after its previous child has drained and
all permits are released. The original error and lane adjustment are logged;
failed temporary output is cleared. Only a complete validated JPEG is published.
Manual failures are returned without tuning/retry. Generic VAAPI decoding error
23, encoding error 24, missing hardware frames, unsupported capabilities, invalid
media and output/quality errors do not establish resource pressure and are not
silently retried. No universal decoder-lane limit is inferred from a VRAM size,
core count, or the observed 15-success/16-failure pair.

Local tests exercise mixed Auto/manual requests, memory costs, growth/backoff,
unknown versus exhausted resources, cancellation and permit cleanup, atomic
configuration requests and validated sprite retry/publication. Host acceptance
requires a separately approved candidate and bounded plan; these tests do not
claim physical B580 output or concurrency acceptance.
