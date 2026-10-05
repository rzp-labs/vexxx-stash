# VAAPI/Vulkan packed RGB export

The application image keeps Alpine's FFmpeg **8.1.2-r0** codecs, filters and
programs. Only `libavutil.so.60.26.102` is rebuilt, using the installed library's
complete configure arguments and the checksum-pinned upstream release source.
The helper rejects a changed package version, source digest, exported symbol set,
ABI version or configure arguments. The runtime gate also checks the actual APK
version, active SONAME target and replacement digest, preventing an old cached
build from silently leaving a newer unpatched library active.

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
