# Self-Hosting Vexxx with Docker

This guide walks you through the process of building and running Vexxx from source using Docker and Docker Compose. This is ideal if you want to deploy Vexxx on a server or NAS.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) installed on your machine
- [Docker Compose](https://docs.docker.com/compose/install/) installed
- Git (to clone the repository)

## Step-by-Step Guide

### 1. Clone the Repository

First, clone the Vexxx repository to your local machine:

```bash
git clone <repository_url>
cd <repository_name>
```

### 2. Configure Volumes (Optional)

In the root of the repository, you'll find a `docker-compose.yml` file. By default, it maps several directories from your host to the container for persistent storage.

You can modify the volume mappings under the `volumes:` section to point to specific directories on your host machine. The format is `/path/on/host:/path/in/container`.

For example, to point the `/data` directory in the container to your actual media collection on your host:

```yaml
    volumes:
      # Keep configs, scrapers, and plugins here.
      - ./config:/root/.stash
      # Point this at your actual media collection
      - /mnt/media/movies:/data
      # Metadata, cache, etc.
      - ./metadata:/metadata
      - ./cache:/cache
      - ./blobs:/blobs
      - ./generated:/generated
```

### 3. Build and Run

With your configuration set up, you can now build the Docker image and start the container. Run the following command from the root of the repository:

```bash
docker-compose up -d --build
```

This process might take a few minutes as it downloads dependencies, builds the frontend UI, and compiles the Go backend.

The `-d` flag runs the container in detached mode, meaning it will run in the background. The `--build` flag ensures that the image is built from the source code.

### 4. Access Vexxx

Once the build is complete and the container is running, you can access Vexxx by opening a web browser and navigating to:

```
http://localhost:9999
```

If you are running this on a remote server, replace `localhost` with the server's IP address (e.g., `http://192.168.1.100:9999`).

## Managing the Container

- **View Logs**: To see the logs for the Vexxx container, run:
  ```bash
  docker-compose logs -f vexxx
  ```

- **Stop the Container**: To stop Vexxx from running, use:
  ```bash
  docker-compose stop
  ```

- **Restart the Container**: To restart Vexxx, use:
  ```bash
  docker-compose restart
  ```

- **Update Vexxx**: To update to the latest code, pull the latest changes from git and rebuild:
  ```bash
  docker-compose up -d --build
  ```

## Port Configuration

By default, Vexxx runs on port `9999`. If you want to change this (for example, if port `9999` is already in use on your machine), you can use environment variables.

Create a `.env` file in the root directory alongside `docker-compose.yml`:

```env
HOST_PORT=8080
STASH_PORT=8080
```

Restart the container with `docker-compose up -d` for the changes to take effect.
