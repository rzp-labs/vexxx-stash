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
with metadata while retaining actual full-range JPEG samples. A final identity
`scale_vaapi` pass copies the sheet into a fresh driver-owned NV12 surface.
Explicit matching BT.601 matrix and limited-range metadata disable passthrough;
size, format, matrix, range and physical samples stay unchanged. This adds one
GPU sheet pass and a native output pool within the existing process, without
CPU pixel transfer or encoding. See the
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

Right-angle rotations and reflected display matrices use VAAPI before scaling.
Supported VR modes use the canonical first-eye 1280×720 GPU projection and
320×180 tiles. Explicit
PQ/HLG Main10 BT.2020 sources receive GPU BT.2390 tone mapping and perceptual
gamut conversion to BT.709 SDR before staged reduction. Both operations use
Vulkan/libplacebo on the selected VAAPI device, then directly import packed RGB
GPU surfaces into VAAPI. Main10 retains ten-bit RGB precision through this bridge.
See [the interoperability patch](../../scripts/ffmpeg/README.md) and
[scene preview eligibility](scene-previews.md).

The final Vulkan-path checks use application binary SHA256 prefix `4799b4d`
and retained-mapping runtime `aa68a4af` (patch `6196ab32`). Two fresh complete
LR180 sheets populated all 81 320×180 cells, producing 2880×1620 JPEGs with
canonical VTT. They completed in 6.999 and 7.011 seconds wall time, using
3.612 and 3.611 media-process CPU seconds. Full decoding and chronological
CPU-cell comparisons preserved projection geometry, source colors and detail.
Observed repeat output identity is a stability check, not a requirement for
compressed-byte equality. TB360, MONO360 and FISHEYE190 three-cell sheets also
passed, with all 78 unused cell centers black. A complete ordinary SDR sheet
passed the common final-copy graph in 6.090 seconds / 3.011 CPU seconds.

Before the terminal identity copy, projected and tone-mapped multi-tile sheets
could fail `mjpeg_vaapi` with VAAPI encoding error 24 despite correct composed
NV12 pixels. The controlled fresh native-surface copy preserved decoded pixels
and allowed encoding; the final integrated sheets above confirm that path.
This is an observed B580/iHD interoperability constraint, not a proven internal
driver cause. The added GPU pass does not resample, change format, range or
encoder quality, and uses no CPU image processing. It remains within the
existing admitted process and adds no independent concurrency cap.

Cancellation after two seconds during an actual LR180 composition/JPEG command
returned in 2.027 seconds, left no final or temporary output or live children,
and reacquired all three configured GPU/total permits in a fresh context. There
was no additional forced benchmark cleanup; normal Go context cancellation
kills its child process. All six render cases and cancellation removed their
disposable containers and recorded no memory, OOM or PID pressure events.

Complete PQ and HLG sheets on the same final build populated all 81 160×90 cells
at 1440×810 with accurate seek requests and VTT. Both fully decoded and retained
ordered neutral patches, highlight detail, hue and ramps across every cell when
compared with the separate proper whole-image CPU tone-mapping policy control.
Tile RGB MAE 2.25–2.49 and PSNR 34.96–35.74 dB are diagnostics, not acceptance
thresholds. Tiny averaged JPEG ramp reversals of at most 0.20/0.24 sample for
PQ/HLG remain recorded; they were not hidden by changing a numeric gate.
The source fixture is static, so these tone controls do not independently prove
source-frame timing; the moving SDR controls above provide that comparison.
The unchanged application CPU reference remains separate from the HDR policy
control. Concurrent HLG preview regressions and HDR metadata checks are recorded
in [scene previews](scene-previews.md).

A combined PQ/LR180 case also passed three populated 320×180 cells with all 78
unused centers black; its proper projected CPU control retains ten-bit precision
until final JPEG conversion. Tile RGB MAE 0.899/PSNR 45.20 dB and the paired MP4
MAE 0.811–0.827/PSNR 45.79–45.93 dB remain diagnostics. This projected HDR policy
control shares the production shader math; the independent canonical SDR VR
comparisons above remain the separate projection oracle.

The final binary/runtime also passed an explicit FISHEYE190 CPU/GPU sprite pair
on the representative 8192×4096 Main10 SDR source. Three widely separated seeks
were 30.125, 1175.1094276666665 and 2320.093855333333 seconds; independent source
packet checks found first eligible PTS 30.130100, 1175.123950 and 2320.101117.
Both outputs fully decoded as 2880×1620 full-range BT.601 JPEGs with three
320×180 tiles, matching projection/content, color and detail and 78 black unused
centers. Software measured 5.112 seconds wall/10.394 CPU seconds; GPU measured
3.142 seconds/1.083 CPU seconds. Peak individual RSS was about 872/319 MB;
the software sampled process-group RSS peak was 2.613 GB. All permits were
reusable and containers removed. GPU and separate RGB24-control cgroup events
were zero; the initial CPU case did not record cgroup event counters.

The canonical VR CPU comparisons have RGB MAE 2.061–2.628 and PSNR 37.343–40.270
dB; occupied-cell SSIM 0.984351 excludes the black canvas. Separate accurate-seek
CPU v360/RGB24 controls corroborate matching content. Those controls were closer
to the CPU JPEG (MAE 1.341–1.831) than the GPU JPEG (2.384–2.708), retained openly.
Inspection found no systematic dark/green bias in this VR CPU reference; the
ordinary raw 8K BMP bias described earlier is not used to dismiss this comparison.
These bounded observations do not establish sustained throughput or new quality
gates. Private media paths and decoded images remain outside the public PR.

Metadata, I/O, orchestration, driver/shader setup, VTT generation and JPEG header
validation still use the CPU. Literal zero CPU usage is impossible. QSV JPEG
and other unsupported
source/capability combinations return explicit errors.
Lossless animated WebP has no supported GPU encoder in the installed stack; it
does not silently switch format, quality or backend. See
[configuration](configuration.md) and [resident MP4 checks](scene-previews.md).
