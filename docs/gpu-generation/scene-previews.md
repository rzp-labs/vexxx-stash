# Scene MP4 previews (VEX-41)

Scene previews have an independent `generation.previews.backend` request
(`software`, the default, or `vaapi`), exposed as `generationPreviewBackend` in
GraphQL and in the existing System generation settings. Saving requires a restart
before activation; it reuses the existing device and process/thread settings.
Software rollback includes this request. Marker and sprite backends remain
independent. VAAPI scene selection suppresses the separate native device pools.

The candidate retains the existing segment/exclusion/scene-limit arithmetic,
short-scene single cut, accurate seeking, 640-pixel aspect
scaling, H.264 High/4.2 MP4, optional AAC128k and stream-copy concat. Segment jobs
run concurrently up to configured GPU/total limits shared with other scenes and
Intel workloads; coordinators never hold permits. Legacy unbudgeted software
remains sequential. Failure cancels and drains siblings before temporary cleanup.
A requested GPU backend returns an explicit error on unsupported input, hardware
capability or generation failure. Software runs only when explicitly selected.
Metadata and packet validation acquire
leaf CPU permits. Source locks remain registered until operation cleanup finishes.

VAAPI decodes to GPU frames, applies `scale_vaapi` in high-quality mode with
concrete even aspect dimensions and NV12 output, then encodes with `h264_vaapi`.
Video pixels remain GPU resident: there is no `hwdownload`, software scale/color
conversion or `hwupload`. Actual-source decode, VAAPI filter and H.264 encoder
probes run before generation. QP21 remains a quality candidate, not CRF21
equivalence; the x264 preset applies only to explicitly selected software.
Container I/O, process orchestration, driver/shader setup, metadata probes,
stream-copy concat and optional AAC audio encoding still require CPU work. Hardware video filtering and
encoding do not make literal zero CPU usage possible.

Eligible inputs have one video stream, unambiguous audio selection and square
SAR: H.264/HEVC 8-bit 4:2:0 SDR, or HEVC Main10 `yuv420p10le` with explicit
BT.709 primaries/matrix/transfer and limited (`tv`) range. Main10 SDR becomes the
existing 8-bit preview through VAAPI VPP conversion. Right-angle display matrices,
including reflections, use `transpose_vaapi`; skew, perspective, nonunit scaling
and arbitrary rotation fail explicitly. Even-height rounding retains the canonical
display aspect ratio through exact SAR metadata.

HEVC Main10 PQ/HLG with explicit BT.2020 matrix/primaries and limited range uses
libplacebo BT.2390 tone mapping, perceptual gamut mapping and peak detection to
BT.709 SDR. Stored LR180, TB360, MONO360 and FISHEYE190 projections use a GPU
shader matching canonical first-eye, 120-degree diagonal-FOV, 1280×720 geometry
and bilinear sampling. Rotation/reflection precedes projection. These operations
derive Vulkan from the selected VAAPI device and retain ten-bit RGB precision
for Main10 until final VAAPI NV12 conversion. Direct DMA-BUF import contains no
CPU pixel staging. Unknown SDR matrix interpretation follows the canonical
BT.601 conversion, while source primaries/transfer tags remain unspecified.
Other unsupported source tags or capabilities fail explicitly. Source timing is
not resampled with an FPS filter. Existing low-frame-rate `vsync=2` handling remains.

Animated scene and marker WebP have no supported GPU lossless animated encoder.
A GPU request returns an explicit capability error without running `libwebp` or
changing the format or lossless settings. Software selection retains the existing
lossless/compression-6 encoding at 12fps and CPU `v360` projection. Logs identify
the requested and actual backend and failure stage/reason. Marker MP4 uses the
same resident VAAPI scaling/encoding path, with QP24 and optional AAC64k. GPU
marker JPEG stills use VAAPI scaling and `mjpeg_vaapi`, without BMP exports or Go
JPEG encoding.
Source-read, sprite, MP4 and requested WebP errors now reach task/job failure reporting
(VEX-21); a successful MP4 is retained when its later WebP fails.

## Focused acceptance, coordinated by the parent

No real GPU execution or performance/quality acceptance is implied by local
command-fixture tests. Do not extend the broad benchmark harness for this change.

1. Agree one authorized real source, the exact reviewed candidate, render device,
   isolated runtime/catalog/output paths, and rollback. Use the actual file's
   source metadata; Main10 support needs an actual Main10 case, not an 8-bit claim.
2. Use the same preview options and configured thread/process ceilings for CPU
   and VAAPI. Initially generate three 0.75-second segments at non-keyframe
   positions spread through the file, audio enabled if present. Use fresh result
   directories; do not replace library artifacts or change production settings.
3. Initial bound: 55 seconds per case plus three seconds cleanup grace,
   one CPU/VAAPI pair first; a second only for a specific observed quality issue.
   Keep the batch within 240 seconds, one scene job at a time. Use an isolated
   existing-image scope with 4 CPUs, 6GiB RAM, no active swap and 256 PIDs. Keep the configured
   segment ceiling (for example 12 GPU slots with total >=12); the three-segment
   case naturally creates at most three segment processes. This is an acceptance
   bound, not a hidden backend cap. Stop on timeout, OOM, instability, lost permits,
   surviving children or incorrect pictures/timing; do not widen bounds silently.
4. Record exact argv, input digest/metadata, FFmpeg/driver/device, selected/actual
   backend and fallback, wall/user+system CPU time, peak RSS, size and cleanup.
   Verify real packets and browser/player playback, correct sampled content at
   each start/end, concat boundaries and total timing, 640/even proportional
   geometry, orientation, color/range, motion/fine detail and audio sync.
5. Compare useful visual quality and material wall/CPU benefit against canonical
   x264 at the existing preview preset/CRF21. Byte-identical lossy streams and
   copied marker SSIM thresholds are not acceptance criteria. Human inspection
   and complete-pipeline measurements decide whether QP21 is acceptable; do not
   repeatedly push parameter changes to tune one fixture. Explicitly record which
   bit depth was checked; one file does not establish both cells.
6. After the short case passes, coordinate one complete preview using the user's
   actual segment count/duration, with a separately agreed deadline if 55 seconds
   is insufficient. A bad device/failure and cancellation check must preserve
   existing output, drain children and allow permit reuse. Verify a requested
   WebP failure reports FAILED while retaining its successful MP4. No sustained
   saturation or mixed production queue is part of this focused acceptance.

Manual settings flow: open System → FFmpeg generation, change only Scene MP4 to
VAAPI, confirm marker/sprite requests and running values stay unchanged, save,
check persisted request/pending restart; after an authorized isolated restart,
check the active snapshot. Select software rollback, save and verify all three
backend requests return to software. Local rendered-control tests cover the
proposal/save behavior; live browser/runtime verification remains separate.

VEX-21 scope: sprite generation errors and preview task errors now reach
`GenerateJob` aggregation. Existing scan and
watcher callers still discard `Start` errors; reporting those through their own
job flows is a separate inherited follow-up, not resolved by this candidate.

## Resident MP4 checks on B580

The isolated candidate (binary SHA256 prefix `6db9c513`) ran with FFmpeg 8.1.2,
iHD 26.2.1 and Intel B580 on two read-only library sources: anonymous 1080p and
4K30 SDR cases. Each CPU/VAAPI pair used three preview segments, slow/CRF21 versus
QP21, H.264 High/4.2 and AAC128k. Configured total/GPU ceilings were 12/12;
generation reached three concurrent segment processes without an added cap.

Both pairs produced 69 video frames at 640x360, with matching video/audio start
times, durations and audio frame counts. The 4K source's explicit BT.709 tags
were retained. Inspection of three corresponding samples from each pair found
matching content timestamps, geometry, colors and detail, with small codec/scaler
rounding differences and no observed contrast or color defect. Commands used
GPU decode, `scale_vaapi` and `h264_vaapi`, without software pixel filtering or
download/reupload. Observed SSIM was 0.985295 for 1080p and 0.982943 for 4K;
these are diagnostic observations, not acceptance thresholds.

Measured media-process CPU time was 2.860235s software versus 0.722151s VAAPI for
1080p, and 2.720573s versus 0.835237s for 4K. Measured wall time was 1.548s versus
0.897s and 1.543s versus 1.074s respectively. CPU orchestration, I/O and audio
remain visible in these measurements. These pairs cover the sampled 8-bit SDR
MP4 paths; the resident Main10 and complete 12-segment results follow below.

The Main10 acceptance binary SHA256 was
`60ea90e9658323b1a7e5b90a62c437baeafdc9f7a52d50ac545ad07848978fef`
(candidate `68de24c2`, with a later comment-only change). It rendered the same
previously authorized 8192x4096 HEVC Main10 SDR source, at 60000/1001 fps with
limited BT.709 tags, one AAC stream, 3465.078283s duration and no catalog VR
projection. The short CPU/VAAPI pair used three 0.75s segments starting at
`30.125 + i * (3434.953283 / 3)` seconds for `i = 0, 1, 2`, with total/GPU
ceilings 12/12 and one FFmpeg thread.

Software wall time was 8.599299s versus 6.884020s VAAPI; media-process CPU time
was 23.213921s versus 1.131692s. Maximum sampled aggregate process-group RSS was
2,587,394,048 versus 778,027,008 bytes, and peak individual-child RSS was
895,111,168 versus 257,597,440 bytes. Aggregate RSS sampling gives lower bounds
on the true peaks; individual-child peaks are a different measurement.

Both short outputs contained 134 video frames at 640x320, limited BT.709, with
0.021s start and 2.251567s video duration. Every video presentation timestamp
and duration matched after ordering by presentation time; differing B-frame
decode order did not affect those comparisons, and DTS was monotonic in both.
All 111 AAC packet timestamps, durations and sizes matched, as did decoded audio
SHA256 (prefix `90d81c2f`). Observed SSIM was 0.986085, used only as a diagnostic.
Three corresponding decoded samples visually matched in color and detail,
without an obvious quality regression.

The user's complete 12-by-0.75s preview also ran on VAAPI, starting at zero over
the 3465.078283s source span, with total/GPU ceilings 12/12 and one FFmpeg thread.
Wall time was 5.551216s and media-process CPU time 3.829303s. Maximum sampled
process-group RSS was 3,054,338,048 bytes, with peak individual-child RSS
257,630,208 bytes. A sampled process group contained the coordinator plus all
12 segment children, confirming that the configured concurrency was used.
The result contained 534 video frames, 9.0049s video / 9.0259s container duration,
640x320 limited BT.709 video and 444 AAC audio frames; complete decoding reported
no FFmpeg errors. No complete 12-segment CPU comparator was rerun, so these
measurements do not establish a full-preview speedup.

These Main10 checks used isolated containers with 4 CPUs, 6GiB RAM, 256 PIDs,
network disabled and read-only media, a 55s execution deadline and 58s cleanup
deadline. The host had no active swap, and all test containers were removed.
Production configuration and library assets were unchanged. Sampled visual QA
and complete decoding cover these specific SDR cases; interactive browser/native
player playback remains separate, as do HDR, VR, WebP and broader source coverage.

Cancellation of the complete 12-segment 8K workload after 750 ms returned
`context canceled` in 0.849 seconds, left no final or temporary output, and needed
no additional forced benchmark cleanup kill. Normal Go context cancellation
uses `exec.CommandContext`'s process kill (SIGKILL on Linux). The fresh-context
drain check reacquired all 12 configured GPU/total
slots after the coordinator returned. This used the final renderer binary
`a2745d0dd91e05c53a82c719c80404f1a8572d8b57b08397896f801525e3c251`;
the subsequent lab-only diagnostic correction does not change the render path.

## Expanded Vulkan-path checks

The forward bridge candidate uses interoperability patch SHA256 prefix
`aa356f7` and runtime library `51060fe`. All four supported synthetic VR
projections produced 69 frames at 640×360 over 2.3 seconds, with presentation
timestamps and durations matching the unchanged CPU comparator. Corresponding
samples across all three segments preserved geometry, color, detail and the
source timer. These small-fixture checks do not establish a library-wide
performance claim or acceptance of the representative 8K VR case.

The representative 8192×4096 Main10 SDR source subsequently passed a three-cut
FISHEYE190 CPU/GPU comparison using binary `80425dd` and retained-mapping runtime
`aa68a4af`. Starts were 30.125, 1175.1094276666665 and 2320.093855333333 seconds,
each 0.75 seconds long, with audio and configured total/GPU limits 12/12.
Both outputs fully decoded 134 frames at 640×360, BT.709 limited range, start
0.021 seconds and duration 2.251567 seconds. Every video presentation timestamp
and duration matched, as did all 111 AAC packet timestamps/durations/sizes and
decoded audio. Nine corresponding samples across the three cuts preserved
projection geometry, color and detail without observed corruption.

This pair measured 8.673591 seconds wall/22.949158 CPU seconds for software and
2.171074 seconds/1.806840 CPU seconds for GPU. Sampled aggregate process-group
RSS peaks were 2,677,321,728 and 878,850,048 bytes, lower bounds rather than exact
high-water marks. Both drained children and released all permits. SSIM 0.979993
is a diagnostic observation, not a quality threshold. These are measurements
of this bounded short pair; they do not establish sustained throughput or a
complete-preview speedup. Complete twelve-cut and active-Vulkan cancellation
checks used the same candidate and are described below.

The complete twelve-cut FISHEYE190 preview fully decoded 534 frames at 640×360,
limited BT.709, with 9.0049 seconds of video and 9.0259 seconds container duration;
all 444 AAC frames decoded without error and video DTS was monotonic. The
observer recorded twelve simultaneous actual libplacebo/H.264 chunk commands,
matching the configured12/12 ceiling. Wall time was 4.407 seconds and media
process CPU time 5.7294 seconds; sampled aggregate RSS peaked around3.407GB.
These are complete-GPU measurements without a complete-CPU speedup comparator.

Cancellation at four seconds observed twelve active chunk renders and CPU
progress, returned `context canceled` in 4.0525 seconds, and left the output
directory empty. No live process-group children remained; the fresh-context
check reacquired all twelve permits. There was no additional forced benchmark
cleanup kill; normal Go context cancellation kills its child processes.
Both cases recorded unchanged zero PID/memory/OOM pressure counters and removed
their disposable containers. These checks cover actual Vulkan rendering rather
than cancellation during capability probes.

Four additional CPU/GPU reflection pairs using application binary `80425dd`
fully decoded 69 frames each with correct orientation and cleared output display
matrices. Landscape output was 640×360/SAR1:1; both portrait reflected rotations
were 640×1138 with exact SAR5121:5120 and DAR9:16, matching the CPU geometry.
Frame-aligned SSIM observations were 0.991250–0.992031, without a new acceptance
threshold. CPU concat timestamps differed from the GPU's uniform 2.3-second
timeline by up to 1.302 milliseconds of time-base rounding; frames and sampled
source content were aligned. These rotation-only checks used library `51060fe`.

The PQ metadata-corrected candidate binary `80425dd` produced 24 frames at
640×360 over 2.4 seconds, with BT.709 limited-range tags and SAR1:1. Full decoding
passed; stream and decoded frames contained no stale HDR side data. Neutral
patches, highlight ordering, hue and ramps matched the separate normalized
whole-image CPU tone-mapping policy control. That control does not replace the
unchanged application CPU comparator.

An earlier concurrent three-segment HLG run crashed one FFmpeg child. Its exact
standalone command and the complete concurrent run both exited normally under
GDB, without a captured crash location. The final retained-mapping build
(`4799b4d` / `aa68a4af`) then passed three uninstrumented repetitions under the
original conditions: three simultaneous render commands, total/GPU limits 3/3,
one FFmpeg thread, 2 CPUs/2 GiB/128 PIDs and the same source/seeks. Each fully
decoded 24 frames at 640×360 over 2.4 seconds, with exact 0.1-second presentation
steps, monotonic DTS, BT.709 limited-range tags and no stale HDR side data.
They completed in 1.27–1.33 seconds. Neutral bands, highlights, hue and ramps
matched the separate proper HLG CPU tone-policy control. No PID, memory or OOM
pressure events occurred; peak observed PID count was 122 of 128. All children
and containers drained and budgets were reusable. These regression repetitions
show no recurrence on the corrected ownership build; the original crash's
internal cause was not captured, and these bounded cases do not prove absence
of every intermittent driver fault.

Complete PQ/HLG JPEG and combined HDR/VR checks use the same final build;
see [sprite checks](resident-sprites.md). Release remains subject to the
parent's merge/publication hold. No CPU pixel fallback is used.

## Historical hybrid Main10 result (before VEX-41)

The preceding hybrid implementation (GPU decode/encode with CPU video scaling)
checked one authorized HEVC Main10 8192x4096 SDR file with three 0.75-second
segments, slow/CRF21 software versus QP21 VAAPI, audio enabled, active total/GPU
ceilings 12/12 and one FFmpeg thread. Production settings/assets were unchanged.
The actual source had square SAR, limited BT.709 tags, one AAC-LC audio stream
and no stored VR projection. The VAAPI diagnostic and recorded commands verified
actual Main10 hardware decode and H.264 hardware encode without fallback. These
measurements do not establish the resident VEX-41 pipeline’s visual acceptance
or CPU/resource behavior.

The whole generated MP4 took 10.435s software versus 7.521s VAAPI (1.39x); media
child CPU time was 29.420s versus 15.135s. Output grew from 153717 to 217517 bytes
(42%). Sampled container memory peaked at 2.308GiB versus 1.264GiB. Sampling is
not an exact aggregate RSS high-water mark; driver child RSS values are per-child.
Both cases naturally reached three FFmpeg segments, under the existing ceiling.

Both outputs were H.264 High/4.2, yuv420p, 640x320, SAR1:1, limited BT.709,
134 video frames with 2.251567s video / 2.272567s container duration. Every video
presentation timestamp and duration matched; all 111 audio packet timestamps,
durations and sizes matched, as did decoded audio SHA256. DTS differences reflect
different encoder reordering; DTS remained monotonic in both. Full decoded-frame
comparison gave SSIM 0.987750 and average PSNR 44.011dB, diagnostic values rather
than invented quality thresholds. Human visual/audio inspection and browser/player
playback remain pending. One source and pair establish neither general speedup nor
8-bit, HDR, VR or twelve-segment acceptance. No second pair/default twelve case ran.
All owned remote test resources were removed, and production idle was confirmed.
