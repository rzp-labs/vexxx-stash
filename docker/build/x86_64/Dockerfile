# syntax=docker/dockerfile:1
# This dockerfile should be built with `make docker-build` from the stash root.

# Build Frontend
FROM node:24-alpine AS frontend
RUN apk add --no-cache make git
## cache node_modules separately
COPY ./ui/v2.5/package.json ./ui/v2.5/pnpm-lock.yaml ./ui/v2.5/pnpm-workspace.yaml /stash/ui/v2.5/
WORKDIR /stash
COPY Makefile /stash/
COPY ./graphql /stash/graphql/
COPY ./ui /stash/ui/
# pnpm install with npm
RUN npm install -g pnpm@10.34.4
RUN make pre-ui
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
RUN --mount=type=secret,id=POSTHOG_CLI_API_KEY,env=POSTHOG_API_KEY,required=${POSTHOG_UPLOAD_REQUIRED} \
    BUILD_DATE=$(date +"%Y-%m-%d %H:%M:%S") make ui-only

# Build Backend
FROM golang:1.26-alpine3.24 AS backend
RUN apk add --no-cache make alpine-sdk nodejs npm \
    && npm install -g @posthog/cli@latest
WORKDIR /stash
COPY ./go* ./*.go Makefile gqlgen.yml .gqlgenc.yml /stash/
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
      make flags-pie flags-posthog-linux STASH_OUTPUT=dist/posthog/stash stash; \
    fi

# Final Runnable Image
FROM alpine:3.24
# Keep the dispatcher, VAAPI driver and QSV GPU runtime on one Alpine release.
RUN apk add --no-cache ca-certificates vips-tools ffmpeg python3 py3-pip \
    intel-media-driver libvpl onevpl-intel-gpu
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
