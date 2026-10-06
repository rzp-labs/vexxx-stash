# GPU interop, strict decoding and metadata

The application image keeps Alpine's FFmpeg **8.1.2-r0** codec/filter libraries
and the `ffprobe` program. `libavutil.so.60.26.102`,
`libavformat.so.62.12.102` and the `ffmpeg` CLI are rebuilt separately,
using the installed library's complete configure arguments and the checksum-pinned
upstream release source. The CLI links the packaged shared libraries instead of
rebuilding codecs or filters; the mapping library patch remains independent.
The helper rejects a changed package version, source digest, exported symbol set,
ABI version or configure arguments. The runtime gate also checks the actual APK
version, active SONAME target and replacement digest, preventing an old cached
build from silently leaving a newer unpatched library active.

`strict-hardware-output.patch` adds the opt-in input option `-hwaccel_strict 1`.
Every selected-GPU generation command requests it together with an explicit
hardware decoder device and hardware output format. Upstream FFmpeg can remove
VAAPI from format negotiation after decoder initialization fails, then select a
software format; libplacebo can upload those CPU-decoded pixels. `-xerror` and
`-hwaccel_output_format vaapi` alone do not prohibit that path. The patched CLI
returns `AV_PIX_FMT_NONE` before selecting software, and also rejects video
decoders without a matching hardware-device configuration before opening them.
Strict inputs use FFmpeg's existing installed-decoder enumeration and require
the requested device type, hardware output format and `HW_DEVICE_CTX` method.
For example, AV1 can select the installed native `av1` decoder's VAAPI
configuration even when the default software decoder is `libdav1d`; there is no
codec or decoder-name whitelist. A missing match fails before opening a decoder,
and an explicitly named incompatible decoder fails rather than being replaced.
Advertised configurations remain subject to the actual driver, profile and
source properties when the first hardware frame is decoded.
The same device/format requirement applies during format negotiation and device
setup, covering decoders which never invoke format negotiation. Invalid strict
settings fail explicitly. Opt-out, automatic acceleration, software playback and
audio preserve upstream behavior.

Strict decoder contexts remain alive until scheduler workers have joined and
filter graphs have released queued hardware frames. Upstream CLI teardown at
decoder EOF can destroy a VAAPI context while libplacebo is synchronizing an
outstanding decoded surface. A captured iHD 26.2.1 fault read an unmapped
completed-count buffer while another thread destroyed that context. The strict
CLI defers this teardown to the existing final decoder cleanup; it retains one
context per decoder and does not change process limits, pixel operations or
software/audio teardown. The build and runtime manifests require this lifetime
correction as well as strict selection.

The application includes the option in probes, frame counts and final renders.
A custom or stock FFmpeg without it rejects the command immediately, rather than
silently retaining upstream fallback. `build-strict-ffmpeg.py` verifies the pinned
source/package, configure arguments, version, advertised codec/filter features and
shared-library dependencies. A separate manifest pins the replacement program
digest; the runtime gate checks the actual executable, APK version and option.
This prevents a cached or alternate binary from masquerading as the strict build.

Stream discovery also needs an explicit policy: upstream
`avformat_find_stream_info` can decode video while probing, before the CLI's
hardware decoder exists. `no-pixel-probe.patch` adds the opt-in format flag
`+no_pixel_probe`. Native H.264/HEVC probing retains header parsing with
`AVDISCARD_ALL`; other video decoders cannot open or receive probe packets.
All decoder-open sites, including EOF probing, enforce the policy. Concat
children inherit it before opening, and child options cannot clear it. Audio
and calls without the flag retain their packaged behavior. GPU source probes
set the flag; generated-output identity/packet checks additionally skip stream
discovery. A stock/custom library missing the flag fails explicitly.

Header-only probing can omit H.264 VUI/SAR or in-band HEVC properties. The CLI's
`-hwaccel_metadata 1` therefore requires strict hardware output and reports the
first actual hardware frame's input stream index, dimensions, underlying pixel
format, component bit depth and RGB flag, SAR, color enums and decoder rate. Bit depth
comes from the actual hardware pool's software-format descriptor, so callers
can preserve precision without a pixel-format-name whitelist. The RGB flag also
comes from that descriptor, independently of the frame's color-matrix tags.
The index is carried from the input
`AVStream`, so callers can associate the automatically selected video with its
header metadata instead of approximating FFmpeg's stream selection.
Generation releases CPU probe admission before acquiring
a bounded GPU permit for this frame, then merges its actual properties before
eligibility and graph planning. Container timing remains authoritative; missing
or invalid frame metadata is an error. No image is downloaded or software
encoded for metadata recovery.

`build-libavformat.py` retains Alpine's existing downstream
`add-av_stream_get_first_dts-for-chromium.patch` (SHA256
`3d8f9994ae8535e370a1ea5bd41a5671346f376a39d55349c5b36977f93a6bb9`).
The pinned Alpine recipe's SHA512 is also verified. This preserves its packaged
DTS getter and exact exported ABI; it does not introduce a new application API.
The helper rejects changes to all other FFmpeg shared libraries. Separate probe
and strict-CLI manifests, active digests, package versions and advertised options
are checked by the assembled GPU-free runtime gate.

`vaapi-vulkan-rgb-export.patch` provides a single-object DMA-BUF bridge from
libplacebo's Vulkan output to VAAPI. On a Vulkan device derived from VAAPI, native
BGRA and X2RGB10 pools with default optimal tiling receive an explicit uncompressed
LINEAR DRM modifier. FFmpeg then collects valid DRM plane descriptors for the
allocation. The previous optimal pool exported a descriptor with zero planes,
which the VAAPI importer rejected. Explicit caller modifier choices and other
device and pixel formats retain their existing allocation behavior. The patch
owns its modifier list and preserves the caller's existing allocation chain.
For X2RGB10, the exporter selects the XRGB2101010 DRM descriptor that VAAPI
accepts. Upstream's generic Vulkan format lookup instead chooses ABGR2101010;
the VAAPI importer rejects it. This correction is confined to X2RGB10 export
and leaves the generic DRM import table unchanged.

The patch also initializes the external layout helper's fallback to ENOTSUP,
allowing the existing GPU ownership barrier to run when host transfer is absent.
Vulkan export already waits for timeline semaphores; its result is now checked and
failures propagate. No pixel data is downloaded or uploaded by this bridge.
The DRM descriptor transfers to its mapping callback only after all FD exports
and format checks succeed. Earlier failures close only successfully exported
FDs and free the descriptor locally. This prevents the callback from retaining
a freed descriptor or closing uninitialized entries during error cleanup.
This correction is separate from decoder lifetime and driver synchronization.
FFmpeg's DMA-BUF mapping callbacks retain the Vulkan frame through the mapped
VAAPI frame's lifetime. The patch also retains the intermediate DRM mapping in
the mapped VAAPI frame's second buffer, while preserving the direct Vulkan
source used for mapping reversal. AVFrame releases the VAAPI import before that
DRM buffer, so the DRM callback imports the consumer fence before releasing its
Vulkan source. Upstream instead releases this callback when flattening the
mapping, before downstream VAAPI work. Intel iHD 26.2.1 Xe imports external BO
batch completion fences as DMA_BUF_SYNC_WRITE, including reader batches;
consumers that publish only reader fences have a different contract. The added
reference makes the fence import boundary explicit; it does not prove that the
old driver path was unsafe or explain an encoder failure.
The process deadline and cancellation bound a stalled driver
wait; the semaphore wait preserves FFmpeg's existing timeout semantics.

Primary source:
- [VAAPI DMA-BUF importer](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/libavutil/hwcontext_vaapi.c)
- [Vulkan allocation and export](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/libavutil/hwcontext_vulkan.c)
- [Intel Xe external BO completion fence](https://github.com/intel/media-driver/blob/intel-media-26.2.1/media_softlet/linux/common/os/xe/mos_synchronization_xe.c)
- [DMA-BUF reader and writer fence semantics](https://github.com/torvalds/linux/blob/v6.12/include/uapi/linux/dma-buf.h)
- [Alpine package recipe](https://gitlab.alpinelinux.org/alpine/aports/-/blob/3.24-stable/community/ffmpeg/APKBUILD)
- [FFmpeg decoder format retry](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/libavcodec/decode.c)
- [FFmpeg CLI hardware selection](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/fftools/ffmpeg_dec.c)
- [libplacebo software-frame upload](https://github.com/haasn/libplacebo/blob/v7.360.1/src/include/libplacebo/utils/libav_internal.h)

The image also packages `mesa-vulkan-intel` and the Vulkan loader; no host driver
or device configuration is changed. The packaging check loads libraries and
checks advertised FFmpeg features without opening a GPU device. Actual rendering
must still pass device probes and isolated output checks. The rendering path uses
VAAPI decode, Vulkan/libplacebo processing, forward VAAPI import and VAAPI
scaling/encoding. A successful exit alone cannot establish correct pixels;
checks must decode the generated result and verify geometry, detail and color,
including RGB channel ordering and retained precision for 10-bit sources.

On the tested B580 with iHD 26.2.1, a Vulkan-processed NV12 sheet could have
correct pre-encode pixels and still fail `mjpeg_vaapi` submission with status 24
(encoding error). A terminal VAAPI VPP copy into a fresh driver-owned surface
passed the controlled JPEG case and preserved its decoded pixels. Matching
width, height, NV12 format, matrix and range keep this an identity operation;
explicit matching output matrix/range disable `scale_vaapi` passthrough. The
cost is one additional GPU sheet pass and output surface within the existing
process, with no CPU pixel transfer or encoding. This is an observed
interoperability workaround, not proof of an internal driver cause; complete
sheet and pixel acceptance must cover the final graph. Neutral default-valued
`procamp_vaapi` and a bare same-size `scale_vaapi` can both pass through and do
not provide this copy.

The relevant passthrough and allocation decisions are in upstream
[scale_vaapi](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/libavfilter/vf_scale_vaapi.c)
and [procamp_vaapi](https://github.com/FFmpeg/FFmpeg/blob/n8.1.2/libavfilter/vf_procamp_vaapi.c).

When upgrading Alpine or FFmpeg, update the pinned package/source version only
after reviewing this patch against the new allocation/export code and repeating the ABI,
packaging and actual GPU import/return checks. Do not drop the gate or overwrite
an unrelated SONAME to make an upgrade pass.
