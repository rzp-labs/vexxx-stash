# Resident Intel sprite rendering

Selecting VAAPI renders JPEG sprite sheets without raw-pixel downloads, BMP
exports, Go image composition or Go JPEG encoding. Software rendering remains
available only when explicitly selected. Device, source, filter and encoder
failures return errors through the sprite task and GenerateJob; they preserve
existing assets instead of starting a software retry.

One hardware decoder reads independently sought ffconcat segments. The demuxer
timestamp origin is included in each inpoint, preroll is excluded, and frame
selection uses timestamps without touching pixels. Each requested timestamp
selects its first eligible frame within the following second; an absent frame
fails rather than substituting another tile. Short clips use an actual GPU
decoded-frame count and the canonical rounded frame indices, including repeated
indices. Unknown timestamp origins and unsupported sources fail explicitly.

VAAPI HQ scaling reduces large sources in stages of approximately two before
producing the concrete 160-pixel, even-height tile. A hardware RGB intermediate
performs source matrix/range conversion; its color metadata is normalized before
GPU conversion to limited-range BT.601 NV12. Row composition and then row stacking
keep all surfaces on the GPU. The normal 9×9 sheet composes at most nine surfaces
per stage, avoiding the observed iHD crash from a single 81-input composition.
This graph structure adds no process-concurrency limit.

Partial sheets extend the canvas with a GPU-generated black sentinel tile and
`xstack_vaapi:fill=black`, avoiding the tiny-source `pad_vaapi` driver failure.
Both the tiny-source and representative 4K partial sheet passed; every unused
tile center was black, and the visible 4K JPEG remained byte-identical to the
staged graph before this composition change.

The final GPU procamp implements the standard limited-to-full sample transform:
`Yfull=(Ylimited-16)*255/219`,
`UVfull=(UVlimited-128)*255/224+128`. The parameters `c=255/219`, `b=-16`,
`s=219/224`, hue zero follow the installed Intel driver's
[procamp equations](https://raw.githubusercontent.com/intel/media-driver/intel-media-26.2.1/media_softlet/agnostic/common/vp/kdll/hal_kerneldll_next.c).
They are not subjective brightness or saturation adjustments. The final encoder
is `mjpeg_vaapi` at global quality 95. Its MPEG-only negotiation is accommodated
with metadata immediately before encoding; no subsequent VPP conversion changes
the actual full-range JPEG samples. See the
[FFmpeg encoder](https://raw.githubusercontent.com/FFmpeg/FFmpeg/n8.1.2/libavcodec/vaapi_encode_mjpeg.c).

## B580 checks

Isolated checks used FFmpeg 8.1.2, iHD 26.2.1 and Intel B580 renderD128, read-only
media and temporary outputs. No production settings or generated assets changed.
Ordinary cases were bounded to 2 CPUs/2 GiB/128 PIDs; the existing representative
8192×4096 Main10 SDR source used 4 CPUs/6 GiB/256 PIDs. Cases had a 55-second
deadline and cleanup grace, with network disabled. The host had no active swap;
Docker reported that swap-limit enforcement was unavailable.

The final staged 81-tile synthetic sheet completed in 5.509 seconds wall time,
2.833 media-process CPU seconds and approximately 115 MB peak child RSS, producing
1440×810 JPEG and canonical VTT. The canonical CPU comparator took 13.081 seconds
and 13.779 CPU seconds. A complete tiny-source frame-sampled sheet also completed,
including repeated frame indices. Timestamp controls compare actual selected
source pictures, not merely VTT timestamps or a reported backend.
The retained canonical comparison measured RGB MAE 6.029, PSNR 25.428 dB and
RGB SSIM 0.810149 for the final synthetic sheet; these are diagnostic results,
not numerical acceptance gates.

Three widely separated samples from the existing 8K Main10 SDR source produced
the expected 1440×720 sheet with 160×80 tiles. CPU generation took 9.787 seconds
wall/9.884 CPU seconds; the staged GPU path took 2.348 seconds/0.675 CPU seconds.
Peak child RSS was approximately 852 MB/282 MB respectively. These measurements
cover these bounded cases, not sustained library throughput.

Quality comparisons retain the unchanged canonical BMP/Go JPEG reference. The
initial GPU range compression and single-pass large-reduction aliasing were
rejected and corrected. Limited/full-range BT.601, BT.709, gray-ramp and mixed-color
controls now preserve expected levels; staged scaling preserves fine detail.
The canonical CPU BMP reduction itself shows a dark/green bias that increases
with the reduction ratio, demonstrated by neutral-gray controls and by comparing
the same source through CPU RGB24 conversion. This bias is not reproduced through
GPU color tuning. Consequently lossy JPEG differences from that legacy reference
remain visible: the 8K samples have RGB MAE about 15.5–16.0 and PSNR about
22.4–22.5 dB. The source RGB24 diagnostic gives GPU mean RGB offsets of about one
sample for the 8K case, RGB MAE 3.71 and PSNR 32.95 dB. The corresponding diagnostic
MAE was 3.86 for 1080p and 4.54 for 4K. These are observations, not new acceptance
thresholds or a replacement for the canonical comparison. Inspection found
matching source content and geometry, comparable detail, and more neutral GPU
colors consistent with the source controls.

Metadata, I/O, orchestration, VTT generation and JPEG header validation still use
the CPU. Literal zero CPU usage is impossible. HDR, VR projection, QSV JPEG and
other unsupported source/capability combinations return explicit errors.
Lossless animated WebP has no supported GPU encoder in the installed stack; it
does not silently switch format, quality or backend. See
[configuration](configuration.md) and [resident MP4 checks](scene-previews.md).
