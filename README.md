# Fileament

**Your 3D files, organized.**

> [!WARNING]
> Fileament is in the early stages of development and has been built with the assistance LLMs. Until this notice is removed, releases may include breaking changes to configuration, storage, the API, or deployment. Back up your data and review the release notes before updating.

Fileament is a self-hosted library for organizing 3D-printing files. Upload models, browse them with thumbnails and a built-in 3D viewer, group them into collections, and create read-only share links without handing your library to a third-party service.

<img width="989" height="607" alt="Screenshot 2026-07-30 at 10 23 51 PM" src="https://github.com/user-attachments/assets/893019aa-f27b-4c96-b858-af5f3f7a7aee" />

It runs as a single container with one persistent `/data` volume. The production image contains one standalone Go executable with the complete web interface embedded.

## Features

- Upload STL, OBJ, 3MF, and ZIP bundles from your browser.
- Keep related mesh variants together and switch between thumbnail previews from the model page.
- Search titles, descriptions, and tags.
- Organize models into ordered collections with custom covers.
- Preview supported meshes in the built-in Three.js viewer.
- Generate local thumbnails in the background without an external rendering service.
- Attach reference images, descriptions, source links, author details, and license information.
- Download individual files or access meshes directly from the model page.
- Create expiring, revocable, read-only links for a model or collection.
- Recover the catalog from durable model and collection sidecars if the SQLite index is lost.
- Use light or dark mode on desktop and mobile.

## Quick start with Docker

Fileament publishes container images for `linux/amd64` and `linux/arm64`.

```sh
docker volume create fileament-data

docker run -d \
  --name fileament \
  --restart unless-stopped \
  -p 8080:8080 \
  -v fileament-data:/data \
  ghcr.io/techhuttv/fileament:latest
```

Open [http://localhost:8080](http://localhost:8080) and create the owner password on the setup screen.

> [!IMPORTANT]
> Complete owner setup before exposing Fileament to an untrusted network. You can seed the first password with `FILEAMENT_OWNER_PASSWORD` if the setup screen will not be reached privately.

The `latest` tag follows the newest stable release. During the early development period, pin a version from [GitHub Releases](https://github.com/TechHutTV/fileament/releases) if you want updates to happen only when you choose.

## Docker Compose

Create `compose.yaml`:

```yaml
services:
  fileament:
    image: ghcr.io/techhuttv/fileament:latest
    container_name: fileament
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - fileament-data:/data

volumes:
  fileament-data:
```

Start Fileament:

```sh
docker compose up -d
```

Open [http://localhost:8080](http://localhost:8080) and finish owner setup.

For a version-pinned deployment, replace `latest` with the release number you want, such as `1.1.0`.

## Configuration

All configuration is optional and provided through environment variables.

| Variable | Default | Purpose |
| --- | --- | --- |
| `FILEAMENT_DATA_DIR` | `/data` | Persistent storage root for the database, models, images, thumbnails, and sidecars. |
| `FILEAMENT_PORT` | `8080` | HTTP port inside the container. |
| `FILEAMENT_OWNER_PASSWORD` | unset | Seeds the owner password on first boot. Ignored after an owner password exists. |
| `FILEAMENT_MAX_UPLOAD_MB` | `2048` | Maximum upload request size and maximum expanded ZIP size in MiB. |
| `FILEAMENT_THUMB_WORKERS` | `2` | Number of background thumbnail workers. |
| `FILEAMENT_BASE_URL` | unset | Public base URL used when displaying share links and determining secure-cookie behavior. |

Example:

```yaml
environment:
  FILEAMENT_BASE_URL: https://models.example.com
  FILEAMENT_MAX_UPLOAD_MB: "4096"
  FILEAMENT_THUMB_WORKERS: "4"
```

`FILEAMENT_WEB_DIR` is only used by untagged development builds. Official production images embed the web interface and do not need a separate static directory.

## Using Fileament

### Upload models

The upload screen supports loose STL, OBJ, and 3MF files plus ZIP archives. Choose **Separate models** to create one model per loose file or **One model with variants** to keep several meshes under one model. File selection stays disabled until one of these organization modes is selected.

ZIP processing rejects unsafe paths and common junk files. Uploads are streamed into the persistent data volume instead of being buffered in memory.

### Organize your library

Use tags for flexible filtering and collections for curated groups. Collections retain their own ordering, descriptions, and cover models.

### Preview and download

Supported meshes can be opened in the browser viewer. Files larger than 50 MB wait for manual confirmation before loading to avoid freezing the browser. Original files remain available for download, and each variation can be renamed inline without changing its file format.

### Share models and collections

Share links are read-only and scoped to one model or collection. They can include an expiration date, track access counts, and be revoked at any time. Shared pages are marked `noindex` for search engines.

## Data and backups

Everything required to restore Fileament lives under `/data`:

```text
/data/
  fileament.db
  collections.json
  tmp/
  models/
    <model-id>/
      model.json
      files/
      images/
      thumbs/
```

Back up the entire volume, not only `fileament.db`. The JSON sidecars preserve model and collection metadata and can rebuild a fresh SQLite index at startup.

For a consistent filesystem-level backup, stop Fileament before copying the volume:

```sh
docker stop fileament
# Back up the fileament-data volume with your normal Docker volume backup tool.
docker start fileament
```

Test restores before relying on a backup process.

## Updating

Back up `/data` and review the [release notes](https://github.com/TechHutTV/fileament/releases) before updating.

With Docker Compose:

```sh
docker compose pull
docker compose up -d
```

If you use a pinned image tag, update the tag in `compose.yaml` first.

With `docker run`, pull the new image, remove the old container, and recreate it with the same volume and configuration. Removing the container does not remove a named volume unless you explicitly delete that volume.

## Reverse proxy and security

Fileament serves HTTP directly. Put it behind a trusted reverse proxy for HTTPS when exposing it beyond a private network.

Set `FILEAMENT_BASE_URL` to the final `https://` URL. Session cookies are always `HttpOnly` and `SameSite=Lax`; they are marked `Secure` when Fileament detects HTTPS through the request, `X-Forwarded-Proto`, or `FILEAMENT_BASE_URL`.

Only explicit share links are public. Owner pages and model assets require an authenticated session.

## Build from source

The Dockerfile is the canonical production build and creates the same standalone binary used by release images:

```sh
git clone https://github.com/TechHutTV/fileament.git
cd fileament
docker build -t fileament:local .

docker run -d \
  --name fileament \
  --restart unless-stopped \
  -p 8080:8080 \
  -v fileament-data:/data \
  fileament:local
```

## Development

Fileament uses Go 1.23 for the backend and React, TypeScript, and Vite for the frontend. See [`AGENTS.md`](AGENTS.md) for the complete architecture, development workflow, security invariants, and verification requirements.

Frontend checks run from `web/`:

```sh
npm ci
npm test
npm run lint
npm run typecheck
npm run build
```

Go checks can run without installing Go on the host:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23-alpine \
  sh -lc 'export PATH="/usr/local/go/bin:$PATH"; go test $(go list ./... | grep -v "/node_modules/"); go vet $(go list ./... | grep -v "/node_modules/")'
```

Generated frontend output is built when needed and must not be committed.

## License

Fileament is licensed under the [GNU Affero General Public License v3.0](LICENSE). See `LICENSE` for the complete terms.
