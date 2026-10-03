# CPU generation admission

Generation configuration can opt into a shared FIFO budget. Missing, zero and `auto` limits resolve conservatively to one; GPU slots cannot exceed total slots. The default existing software configuration retains legacy scheduling. An explicit budget applies to individual subprocess and composition stages rather than whole scene tasks, avoiding nested admission deadlocks.

Metadata probes, frame counting, FFmpeg generation, canonical CPU pHash and sprite publishing use cancellable admission. Probe execution deadlines start after admission. Cancellation and failed encoding release permits and preserve existing sprite output. Canonical pHash retains its existing seek, scale and hash algorithm; under the shared budget its frame decoder concurrency is one. Stored hashes are not migrated.

The Windows native pipeline uses separate pools, so it is bypassed when a shared generation budget is selected. Decoder, filter and encoder thread controls limit their respective FFmpeg stages; a configured thread count is not an operating-system CPU quota. Images and unrelated scanning or plugins are not covered by every media-generation limit.

Animated WebP stays on CPU and uses the corrected explicit lossless/compression-6 settings. Lossless output costs more CPU than the historical preset-overridden lossy output; that historical output must not serve as its performance baseline. Intel workload integration follows in later stack increments.
