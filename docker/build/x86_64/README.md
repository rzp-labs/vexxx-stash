# Introduction

This Dockerfile builds the complete Vexxx container from source, including Python,
FFmpeg, libvips, and the AI service clients. GitHub Actions uses this same file to
publish tested Linux amd64 images to `ghcr.io/rzp-labs/vexxx-stash`.
See the [deployment instructions](../../production/README.md) to pull an image
with Docker Compose.

# Building the docker container

From the top-level directory (should contain `tools.go` file):

```
make docker-build

```

The build explicitly targets `linux/amd64`, including on Apple Silicon Macs.
Docker Desktop uses emulation on those machines; Linux x86 servers run natively.

# Running the docker container

## Using docker-compose

See the `README.md` file in `docker/production` for instructions on how to get docker-compose if needed.

To run the local `stash/build` image with the Compose file in `docker/production`,
set `STASH_IMAGE=stash/build:latest` in that directory's `.env` and run
`docker compose up -d stash` without pulling.

## Using `docker run`

After building the container:

```
docker run \
 -e STASH_STASH=/data/ \
 -e STASH_METADATA=/metadata/ \
 -e STASH_CACHE=/cache/ \
 -e STASH_GENERATED=/generated/ \
 -v <path to config dir>:/root/.stash \
 -v <path to media>:/data \
 -v <path to metadata>:/metadata \
 -v <path to cache>:/cache \
 -v <path to generated>:/generated \
 -p 9999:9999 \
 stash/build:latest 
```

## Easy Setup Scripts (Recommended)

We provide standalone scripts that walk you through mounting your media libraries:

### Windows

```batch
cd docker\build\x86_64
setup-stash.bat
```

### Linux / macOS

```bash
cd docker/build/x86_64
chmod +x setup-stash.sh
./setup-stash.sh
```

The scripts will:
1. Prompt for your config directory (where Stash stores its database)
2. Prompt for generated files directory (thumbnails, transcodes)
3. Let you add multiple media library paths
4. Generate a `docker-compose.generated.yml` and/or a `docker run` command

After running the script, start Stash with:

```bash
docker compose -f docker-compose.generated.yml up -d
```

### Path Mapping

Your host paths are mapped to container paths like this:

| Host Path | Container Path |
|-----------|----------------|
| Config dir | `/root/.stash` |
| Generated dir | `/generated` |
| Media library 1 | `/data/library1` |
| Media library 2 | `/data/library2` |
| ... | ... |

**Important**: When adding libraries in Stash's UI, use the *container* paths (e.g., `/data/library1`).

### Adding More Libraries Later

To add new media libraries to an existing setup:

1. Stop the container: `docker stop stash`
2. Remove it: `docker rm stash`
3. Re-run the setup script with all paths (existing + new)
4. Start again: `docker compose -f docker-compose.generated.yml up -d`

Your Stash data (database, config) is preserved because it lives on the host in your config directory.

---

## Docker Compose (manual / build from source)

You can also use the provided `docker-compose.yml` to build the image from source:

```bash
# copy example env
cp .env.example .env

# build and run in detached mode
docker compose up --build -d
```

Configuration notes:
- Edit `.env` to set `HOST_CONFIG`, `HOST_MEDIA_PATH`, and `HOST_STORAGE` to host directories you want mounted into the container.
- On Windows use absolute paths in `.env` (e.g. `C:\Users\you\Media`). If using Docker Desktop make sure the drive is shared.

If you run into permission issues on Linux, set `PUID` and `PGID` in `.env` to match your host user so files created by the container are accessible.


Change the `<xxx>` to the appropriate paths. Note that the `<path to media>` directory should be separate from the cache, generated and metadata directories. It is recommended to have the cache, generated and metadata directories in the same parent directory, for example:

```
/stash
  /config
  /metadata
  /generated
  /cache
/media
```

Using this example directory structure, the above command would be:

```
docker run \
 -e STASH_STASH=/data/ \
 -e STASH_METADATA=/metadata/ \
 -e STASH_CACHE=/cache/ \
 -e STASH_GENERATED=/generated/ \
 -v /stash/config:/root/.stash \
 -v /media:/data \
 -v /stash/metadata:/metadata \
 -v /stash/cache:/cache \
 -v /stash/generated:/generated \
 -p 9999:9999 \
 stash/build:latest 
```
