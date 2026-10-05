# syntax=docker/dockerfile:1
# This dockerfile should be built with `make docker-build` from the stash root.

# Rebuild only libavutil with the exact packaged feature configuration. The
# source digest, package version and exported ABI are checked by the helper.
FROM alpine:3.24 AS ffmpeg-mapping
RUN apk add --no-cache ffmpeg=8.1.2-r0 ffmpeg-libavutil=8.1.2-r0 python3 curl xz build-base pkgconf linux-headers \
    alsa-lib-dev aom-dev bzip2-dev coreutils dav1d-dev fontconfig-dev freetype-dev \
    fribidi-dev harfbuzz-dev ladspa-dev lame-dev libass-dev libbluray-dev libdrm-dev \
    libdvdnav-dev libdvdread-dev libjxl-dev libopenmpt-dev libplacebo-dev librist-dev \
    libsrt-dev libssh-dev libtheora-dev libva-dev libvdpau-dev libvorbis-dev libvpx-dev \
    libwebp-dev libxfixes-dev libxml2-dev lilv-dev nasm openssl-dev opus-dev perl-dev \
    pulseaudio-dev rav1e-dev shaderc-dev sdl2-dev soxr-dev v4l-utils-dev vidstab-dev \
    vulkan-loader-dev x264-dev x265-dev xvidcore-dev zeromq-dev zimg-dev zlib-dev \
    svt-av1-dev libvpl-dev
WORKDIR /ffmpeg
COPY scripts/ffmpeg/build-libavutil.py scripts/ffmpeg/vaapi-vulkan-rgb-export.patch /ffmpeg/
RUN curl --fail --silent --show-error --location \
    https://ffmpeg.org/releases/ffmpeg-8.1.2.tar.xz -o /ffmpeg/source.tar.xz && \
    python3 build-libavutil.py /ffmpeg/source.tar.xz /ffmpeg/vaapi-vulkan-rgb-export.patch /ffmpeg/output

# Probe policy is independently cached; codec and filter libraries stay packaged.
FROM ffmpeg-mapping AS ffmpeg-probe
RUN apk add --no-cache ffmpeg-dev=8.1.2-r0
COPY scripts/ffmpeg/build-libavformat.py scripts/ffmpeg/no-pixel-probe.patch \
    scripts/ffmpeg/add-av_stream_get_first_dts-for-chromium.patch /ffmpeg/
RUN python3 build-libavformat.py /ffmpeg/source.tar.xz /ffmpeg/no-pixel-probe.patch /ffmpeg/probe-output

# Keep the strict CLI layer separate so decoder policy changes reuse libraries.
FROM ffmpeg-probe AS ffmpeg-strict
COPY scripts/ffmpeg/build-strict-ffmpeg.py scripts/ffmpeg/strict-hardware-output.patch /ffmpeg/
RUN cp /ffmpeg/probe-output/libavformat.so.62.12.102 /usr/lib/libavformat.so.62.12.102 && \
    python3 build-strict-ffmpeg.py /ffmpeg/source.tar.xz /ffmpeg/strict-hardware-output.patch /ffmpeg/strict-output

# Build Frontend
FROM node:24-alpine AS frontend
RUN apk add --no-cache make git
## cache node_modules separately
COPY ./ui/v2.5/package.json ./ui/v2.5/pnpm-lock.yaml ./ui/v2.5/pnpm-workspace.yaml /stash/ui/v2.5/
WORKDIR /stash
COPY Makefile /stash/
# pnpm install with npm
RUN npm install -g pnpm@10.34.4
RUN make pre-ui
COPY ./graphql /stash/graphql/
COPY ./ui /stash/ui/
RUN make generate-ui
ARG GITHASH
ARG STASH_VERSION
ARG POSTHOG_UPLOAD_REQUIRED=false
ARG POSTHOG_CLI_PROJECT_ID
ARG POSTHOG_CLI_HOST
ARG VITE_PUBLIC_POSTHOG_PROJECT_TOKEN
ARG VITE_PUBLIC_POSTHOG_HOST
ARG GITHUB_ACTIONS
ARG GITHUB_SHA
ARG GITHUB_REF_NAME
ARG GITHUB_REPOSITORY
ARG GITHUB_SERVER_URL
ENV POSTHOG_PROJECT_ID=$POSTHOG_CLI_PROJECT_ID \
    POSTHOG_HOST=$POSTHOG_CLI_HOST \
    POSTHOG_UPLOAD_REQUIRED=$POSTHOG_UPLOAD_REQUIRED \
    VITE_PUBLIC_POSTHOG_PROJECT_TOKEN=$VITE_PUBLIC_POSTHOG_PROJECT_TOKEN \
    VITE_PUBLIC_POSTHOG_HOST=$VITE_PUBLIC_POSTHOG_HOST \
    GITHUB_ACTIONS=$GITHUB_ACTIONS \
    GITHUB_SHA=$GITHUB_SHA \
    GITHUB_REF_NAME=$GITHUB_REF_NAME \
    GITHUB_REPOSITORY=$GITHUB_REPOSITORY \
    GITHUB_SERVER_URL=$GITHUB_SERVER_URL
ARG BUILD_DATE
# Keep master's bounded frontend heap and deterministic build date.
RUN --mount=type=secret,id=POSTHOG_CLI_API_KEY,env=POSTHOG_API_KEY,required=${POSTHOG_UPLOAD_REQUIRED} \
    NODE_OPTIONS=--max-old-space-size=4096 \
    BUILD_DATE="${BUILD_DATE:-$(date +"%Y-%m-%d %H:%M:%S")}" make ui-only

# Build Backend
FROM golang:1.26-alpine3.24 AS backend
ENV GOMAXPROCS=4 GOFLAGS=-p=4 GOMEMLIMIT=2GiB
RUN apk add --no-cache make alpine-sdk nodejs npm \
    && npm install -g @posthog/cli@0.16.2
WORKDIR /stash
COPY go.mod go.sum /stash/
RUN go mod download
COPY ./*.go Makefile gqlgen.yml .gqlgenc.yml /stash/
COPY ./graphql /stash/graphql/
COPY ./scripts /stash/scripts/
COPY ./pkg /stash/pkg/
COPY ./cmd /stash/cmd/
COPY ./internal /stash/internal/
# needed for generate-login-locale
COPY ./ui /stash/ui/
RUN make generate-backend generate-login-locale
COPY --from=frontend /stash /stash/
ARG GITHASH
ARG STASH_VERSION
ARG BUILD_DATE
ENV BUILD_DATE=$BUILD_DATE
ARG POSTHOG_UPLOAD_REQUIRED=false
ARG POSTHOG_CLI_PROJECT_ID
ARG POSTHOG_CLI_HOST
ARG GITHUB_ACTIONS
ARG GITHUB_SHA
ARG GITHUB_REF_NAME
ARG GITHUB_REPOSITORY
ARG GITHUB_SERVER_URL
ENV POSTHOG_CLI_PROJECT_ID=$POSTHOG_CLI_PROJECT_ID \
    POSTHOG_CLI_HOST=$POSTHOG_CLI_HOST \
    GITHUB_ACTIONS=$GITHUB_ACTIONS \
    GITHUB_SHA=$GITHUB_SHA \
    GITHUB_REF_NAME=$GITHUB_REF_NAME \
    GITHUB_REPOSITORY=$GITHUB_REPOSITORY \
    GITHUB_SERVER_URL=$GITHUB_SERVER_URL
RUN --mount=type=secret,id=POSTHOG_CLI_API_KEY,env=POSTHOG_CLI_API_KEY,required=${POSTHOG_UPLOAD_REQUIRED} \
    if [ "$POSTHOG_UPLOAD_REQUIRED" = "true" ]; then \
      make build-release-posthog-linux; \
    else \
      mkdir -p dist/posthog && \
      make flags-pie flags-posthog-linux STASH_OUTPUT="-o dist/posthog/stash" stash; \
    fi

# Final Runnable Image
FROM alpine:3.24
# Keep the dispatcher, VAAPI driver and QSV GPU runtime on one Alpine release.
RUN apk add --no-cache ca-certificates vips-tools ffmpeg=8.1.2-r0 ffmpeg-libavutil=8.1.2-r0 python3 py3-pip \
    intel-media-driver libvpl onevpl-intel-gpu mesa-vulkan-intel vulkan-loader
COPY --from=ffmpeg-mapping /ffmpeg/output/libavutil.so.60.26.102 /usr/lib/libavutil.so.60.26.102
COPY --from=ffmpeg-mapping /ffmpeg/output/mapping-build.json /usr/share/vexxx/ffmpeg-mapping-build.json
COPY --from=ffmpeg-probe /ffmpeg/probe-output/libavformat.so.62.12.102 /usr/lib/libavformat.so.62.12.102
COPY --from=ffmpeg-probe /ffmpeg/probe-output/probe-build.json /usr/share/vexxx/ffmpeg-probe-build.json
COPY --from=ffmpeg-strict /ffmpeg/strict-output/ffmpeg /usr/bin/ffmpeg
COPY --from=ffmpeg-strict /ffmpeg/strict-output/strict-build.json /usr/share/vexxx/ffmpeg-strict-build.json
COPY scripts/check_intel_runtime.py /usr/local/bin/check_intel_runtime.py
# Packaging regression check; does not require or initialize a GPU.
RUN python3 /usr/local/bin/check_intel_runtime.py
# Install Python dependencies for AI services (StashFace, StashTag, MegaFace)
RUN pip3 install --no-cache-dir --break-system-packages gradio_client==1.8.0
COPY --from=backend /stash/dist/posthog/stash /usr/bin/
# Copy Python AI service scripts
COPY --from=backend /stash/pkg/stashface/client.py /usr/lib/stash/stashface/client.py
COPY --from=backend /stash/pkg/stashtag/client.py /usr/lib/stash/stashtag/client.py
COPY --from=backend /stash/pkg/megaface/client.py /usr/lib/stash/megaface/client.py
RUN mkdir -p /root/.stash
ENV STASH_CONFIG_FILE=/root/.stash/config.yml
# Set Docker container environment variable for detection
ENV DOCKER_CONTAINER=1
# Set Python script paths for the AI services
ENV STASH_STASHFACE_SCRIPT=/usr/lib/stash/stashface/client.py
ENV STASH_STASHTAG_SCRIPT=/usr/lib/stash/stashtag/client.py
ENV STASH_MEGAFACE_SCRIPT=/usr/lib/stash/megaface/client.py
# Default port — override at runtime with -e STASH_PORT=<port> and update your port mapping.
ENV STASH_PORT=9999
EXPOSE ${STASH_PORT}
ENTRYPOINT ["stash"]
