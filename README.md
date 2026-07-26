# Fileament

Fileament is a self-hosted catalog for a personal 3D printing model library. It runs as one Go binary in one container with one persistent `/data` volume.

## Features

- Owner-only administration with Argon2id password hashing and SQLite-backed sessions.
- Upload STL, OBJ, 3MF, or ZIP bundles from the web UI.
- Streamed multipart upload ingestion into `/data/tmp`; no host `/tmp` staging or multipart memory buffering.
- Secure ZIP extraction with junk-file filtering, path traversal rejection, compressed request caps, and uncompressed ZIP caps.
- SQLite catalog with FTS search, tags, cursor pagination, collections, and durable `model.json` sidecars.
- Background thumbnail jobs with a software JPEG rasterizer and SSE thumbnail completion events.
- Embedded React/Vite/TypeScript UI with dark mode, lazy thumbnail loading, detail views, downloads, and a Three.js viewer with a 50 MB auto-load gate.
- Owner assets under `/files`, `/mesh`, `/thumbs`, and `/images` require authentication.
- Read-only public share links for models or collections with expiry, revoke, hit counts, `noindex`, and token-scoped file, mesh, image, and thumbnail routes.

## Run With Docker

```sh
docker build -t fileament .
docker run --rm -p 8080:8080 \
  -e FILEAMENT_OWNER_PASSWORD='change-this-password' \
  -v fileament-data:/data \
  fileament
```

Open `http://localhost:8080`.

The compose example does the same with a named volume:

```sh
docker compose up --build
```

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `FILEAMENT_DATA_DIR` | `/data` | Storage root for SQLite, uploaded files, thumbnails, and sidecars. |
| `FILEAMENT_PORT` | `8080` | HTTP listen port inside the container. |
| `FILEAMENT_OWNER_PASSWORD` | unset | Seeds the owner password on first boot. If unset, use the setup screen. |
| `FILEAMENT_MAX_UPLOAD_MB` | `2048` | Per-request upload cap and ZIP uncompressed-size cap. |
| `FILEAMENT_THUMB_WORKERS` | `2` | Thumbnail worker concurrency. |
| `FILEAMENT_BASE_URL` | unset | Optional base URL for displaying share URLs. |

Set `FILEAMENT_BASE_URL` to an `https://` URL, or serve Fileament over TLS, to mark owner session cookies `Secure`. Cookies are always `HttpOnly` and `SameSite=Lax`.

## API Summary

Owner routes require the session cookie:

```text
POST   /api/auth/setup
POST   /api/auth/login
POST   /api/auth/logout
POST   /api/auth/password
GET    /api/me

GET    /api/models?q=&tag=&collection=&sort=&cursor=&limit=
POST   /api/models
GET    /api/models/{id}
PATCH  /api/models/{id}
DELETE /api/models/{id}
POST   /api/models/{id}/files
DELETE /api/models/{id}/files/{fid}
POST   /api/models/{id}/images
DELETE /api/models/{id}/images/{imageID}
PUT    /api/models/{id}/thumb

GET    /api/collections
POST   /api/collections
GET    /api/collections/{id-or-slug}
PATCH  /api/collections/{id}
DELETE /api/collections/{id}
PUT    /api/collections/{id}/models/{mid}
DELETE /api/collections/{id}/models/{mid}

GET    /api/shares
POST   /api/shares
DELETE /api/shares/{id}
GET    /api/storage
GET    /api/events

GET    /files/{modelID}/{fid}
GET    /mesh/{modelID}/{fid}
GET    /thumbs/{modelID}/{name}
GET    /images/{modelID}/{imageID}
```

Public routes are token-scoped and return `X-Robots-Tag: noindex`:

```text
GET /api/public/{token}
GET /api/public/{token}/files/{fid}
GET /api/public/{token}/mesh/{fid}
GET /api/public/{token}/thumbs/{name}?model={modelID}
GET /api/public/{token}/images/{imageID}
```

The initial model upload remains `POST /api/models`; append mesh files, ZIP bundles, and images through the model-specific routes after creation.

## Development

The host environment for this repo does not need Go installed. Run Go commands through Docker:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23-alpine go test ./...
```

The CI command excludes `node_modules` from Go package discovery:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23-alpine \
  sh -lc 'export PATH="/usr/local/go/bin:$PATH"; go test $(go list ./... | grep -v "/node_modules/")'
```

Frontend commands run from `web/` with Node 22:

```sh
npm ci
npm run lint
npm run typecheck
npm test
npm run build
```

`npm run build` writes the Vite bundle into `internal/server/dist`, which is embedded into the Go binary.

## Storage Layout

```text
/data/
  fileament.db
  tmp/
  models/
    <model-id>/
      model.json
      files/
      images/
      thumbs/
```

SQLite is the query index. `model.json` is written beside each model on metadata changes so the durable catalog record lives with the uploaded files.
