# Run Vexxx with Docker Compose

The published image is `ghcr.io/rzp-labs/vexxx-stash`. It includes the frontend,
backend, FFmpeg, libvips, Python, and the AI service clients. Images target
`linux/amd64` for x86 Linux servers. Docker Desktop on Apple Silicon Macs runs
the same image using emulation.

## Install in a homelab

Install [Docker Engine and the Compose plugin](https://docs.docker.com/engine/install/).
Download the deployment files into your service's persistent data directory:

```sh
mkdir vexxx && cd vexxx
curl -fsSLO https://raw.githubusercontent.com/rzp-labs/vexxx-stash/master/docker/production/docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/rzp-labs/vexxx-stash/master/docker/production/.env.example -o .env
```

Edit the volume paths in `docker-compose.yml` to point at your collection and
persistent storage. The paths on the left are host directories; paths on the
right are container directories. Keep the config/database, metadata, cache,
blobs, and generated content on persistent mounts.

Select an image in `.env`:

```dotenv
# Existing edge image (no longer updated by master pushes):
IMAGE_TAG=edge

# Deliberately published application candidate (updated only by manual opt-in):
# IMAGE_TAG=latest

# Select an actually published normal release or its commit tag:
# These examples show the naming format, not available versions.
# IMAGE_TAG=v1.2.3
# IMAGE_TAG=sha-<full 40-character commit SHA>

# For an immutable reference, override the entire image instead:
# STASH_IMAGE=ghcr.io/rzp-labs/vexxx-stash@sha256:<digest>

# Deliberately published manual test images use a separate package:
# STASH_IMAGE=ghcr.io/rzp-labs/vexxx-stash-test:test-candidate-123456789-1
```

`STASH_IMAGE` takes precedence over `IMAGE_TAG`. A digest is immutable; release
and commit tags identify builds but can be overwritten if republished. Multiple
release tags for the same commit can overwrite its SHA tag with a different
embedded version. The existing `edge` default is retained for compatibility;
it stops updating under the publication policy below. Choose an actually
published release, candidate/latest image, test image, or digest explicitly when upgrading.

Public packages need no login. For private packages, log in on the server with a
GitHub personal access token (classic) with `read:packages` and package access.
Authorize organization SSO if required:

```sh
# In Bash; enter the token without displaying it.
read -rsp 'GitHub token: ' GHCR_TOKEN
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
unset GHCR_TOKEN
```

Start the service:

```sh
docker compose pull stash
docker compose up -d stash
```

Open `http://YOUR-SERVER-IP:9999`. Set `HOST_PORT` and `STASH_PORT` in `.env` to
change the host and container ports. Use container paths such as `/data` when
adding libraries in Vexxx. For remote access, configure a
[reverse proxy](https://docs.stashapp.cc/guides/reverse-proxy/).

## Updates and rollback

Back up the config/database before upgrading. Select the new release, SHA tag,
or digest in `.env`, then run:

```sh
docker compose pull stash
docker compose up -d stash
docker compose logs --tail 100 stash
```

Updates preserve the mounted data. To restore an older version, select its
reference and repeat these commands. Database migrations may require restoring
the matching backup as well. Avoid `docker compose down --volumes` when retaining
named volumes.

## Building and publishing

[The GitHub Actions workflow](../../.github/workflows/docker-publish.yml) always
checks the publication policy and Intel packaging tests. Ready application PRs
and master landings run affected backend/frontend/Python checks and production
compilation. Ordinary application edits do not build images. Actual packaging
inputs and explicit manual/release candidates build and smoke-test Linux amd64
images. Drafts defer substantive validation; the always-present `ci-required`
aggregate checks every planned obligation and fails deferred drafts. See
[local validation before push](../../docs/LOCAL_FIRST_CI.md) for the existing
commands and classification rules.

Publication requires a deliberate release tag or manual choice:

- Pushes to `master` and pull requests validate relevant changes and never publish.
- Normal release tags must be exactly `vMAJOR.MINOR.PATCH`, with no leading zeros,
  prerelease suffix, or build metadata (for example, the format `v1.2.3`). After
  full validation, they publish the exact tag and `sha-<full commit SHA>` in
  `ghcr.io/rzp-labs/vexxx-stash`.
- Manual runs validate and smoke-test by default. To publish a test image,
  deliberately enable `publish_test_image` and supply `test_label`: 1–32 lowercase
  letters/digits separated by single hyphens. These runs publish only to
  `ghcr.io/rzp-labs/vexxx-stash-test:test-<label>-<run ID>-<attempt>`, never the
  release package, `edge`, or `latest`.
- Manual master runs may instead explicitly enable `publish_latest` and supply
  the same validated candidate label. They publish the tested image to
  `ghcr.io/rzp-labs/vexxx-stash:candidate-<label>-<run ID>-<attempt>` and tag those
  same bytes as `ghcr.io/rzp-labs/vexxx-stash:latest`. The candidate tag is its
  baked application version. Test/latest publication are mutually exclusive and
  default off; neither ordinary master pushes nor release tags update latest.

Validation and image construction use a read-only `GITHUB_TOKEN`. Only the
separate publication job gets `packages: write` (plus `actions: read` for the
tested artifact); it checks the image archive checksum, source revision, version and platform,
then uploads the exact smoke-tested image without rebuilding or executing
repository scripts. No PAT is needed in repository secrets. After the first
successful publication of either package, set its visibility in GitHub's package
settings. Packages may initially be private even when the repository is public.
Organization settings must allow the workflow to create/write packages. The
source label links each image to the repository.

For a local build on Linux or an Apple Silicon Mac:

```sh
make docker-build
```

This also targets `linux/amd64`. To run it with this Compose file, set
`STASH_IMAGE=stash/build:latest` and run `docker compose up -d stash` without
pulling. Emulated builds on Apple Silicon take longer than native x86 builds.
