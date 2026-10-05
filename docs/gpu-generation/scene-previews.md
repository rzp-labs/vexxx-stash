# Scene MP4 previews (VEX-27)

Scene previews have an independent `generation.previews.backend` request
(`software`, the default, or `vaapi`), exposed as `generationPreviewBackend` in
GraphQL and in the existing System generation settings. Saving requires a restart
before activation; it reuses the existing device and process/thread settings.
Software rollback includes this request. Marker and sprite backends remain
independent. VAAPI scene selection suppresses the separate native device pools.

The candidate retains the existing segment/exclusion/scene-limit arithmetic,
short-scene single cut, accurate seeking, slow-seek recovery, 640-pixel aspect
scaling, H.264 High/4.2 MP4, optional AAC128k and stream-copy concat. Segment jobs
run concurrently up to configured GPU/total limits shared with other scenes and
Intel workloads; coordinators never hold permits. CPU fallback uses the configured
total limit. Legacy unbudgeted software remains sequential. Failure cancels and
drains siblings before temporary cleanup, concat or a whole-preview CPU retry.
No hardware and software chunks are mixed. Metadata and packet validation acquire
leaf CPU permits. Source locks remain registered until operation cleanup finishes.

VAAPI decodes to GPU frames, downloads full-resolution NV12/P010, restores planar
source precision, applies canonical CPU scale/pixel conversion, uploads NV12 and
encodes with `h264_vaapi`. CPU scaling/transfer remain; expensive `libx264` video
encoding is replaced. QP21 is an initial quality candidate, not CRF21 equivalence;
the x264 preset remains a software control. The candidate probes actual-source
decode, download, filter/upload and H.264 encoding before the scene attempt.

Eligible inputs: one video stream, unambiguous audio selection, square SAR, no
rotation, H.264/HEVC 8-bit 4:2:0 SDR; additionally HEVC Main10 `yuv420p10le` with
explicit BT.709 primaries/matrix/transfer and limited (`tv`) range. Main10 becomes
the existing 8-bit preview through canonical swscale conversion. This is not HDR
tonemapping. Other 10-bit tags, HDR/wide gamut, rotation, non-square SAR, multiple
video/audio streams and VR use canonical software with a diagnostic reason. Known
color tags are retained; unspecified tags are not invented. Source timing is not
resampled with an FPS filter. Existing low-frame-rate `vsync=2` handling remains.

Animated scene WebP still uses CPU lossless/compression-6 encoding at 12fps.
VR projection still uses the existing CPU `v360` path. Logs identify scene MP4
selected/actual backend and fallback stage/reason, and identify WebP as CPU work.
Source-read, MP4 and requested WebP errors now reach task/job failure reporting
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

VEX-21 scope: task errors now reach `GenerateJob` aggregation. Existing scan and
watcher callers still discard `Start` errors; reporting those through their own
job flows is a separate inherited follow-up, not resolved by this candidate.

## First isolated Main10 result

One authorized HEVC Main10 8192x4096 SDR file was checked with three 0.75-second
segments, slow/CRF21 software versus QP21 VAAPI, audio enabled, active total/GPU
ceilings 12/12 and one FFmpeg thread. Production settings/assets were unchanged.
The actual source had square SAR, limited BT.709 tags, one AAC-LC audio stream
and no stored VR projection. The VAAPI diagnostic and recorded commands verified
actual Main10 hardware decode and H.264 hardware encode without fallback.

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
