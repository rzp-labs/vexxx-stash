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
# Latest tested build from master:
IMAGE_TAG=edge

# Or select a published release or commit:
# IMAGE_TAG=v1.2.3
# IMAGE_TAG=sha-<full 40-character commit SHA>

# For an immutable reference, override the entire image instead:
# STASH_IMAGE=ghcr.io/rzp-labs/vexxx-stash@sha256:<digest>
```

`STASH_IMAGE` takes precedence over `IMAGE_TAG`. A digest is immutable; release
and commit tags identify builds but can be overwritten if republished.

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

[The GitHub Actions workflow](../../.github/workflows/docker-publish.yml) runs Go
unit/integration tests, frontend tests, and the TypeScript check, then builds and
smoke-tests the Linux amd64 container. Only successful builds are published:

- Pushes to `master` publish `edge` and `sha-<full commit SHA>`.
- Pushes of `v*` tags publish the exact tag and a SHA tag.
- Manual runs publish a SHA tag, plus `edge` when run on `master`.
- Pull requests run the checks and container smoke test without publishing.

The workflow authenticates with `GITHUB_TOKEN` using `packages: write`; no PAT is
needed in repository secrets. After the first successful publication, set the
package's visibility in GitHub's package settings. Packages may initially be
private even when the repository is public. Organization settings must allow
the workflow to create/write packages. The source label links the image to the
repository.

For a local build on Linux or an Apple Silicon Mac:

```sh
make docker-build
```

This also targets `linux/amd64`. To run it with this Compose file, set
`STASH_IMAGE=stash/build:latest` and run `docker compose up -d stash` without
pulling. Emulated builds on Apple Silicon take longer than native x86 builds.
