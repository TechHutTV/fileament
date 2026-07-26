# Fileament

Fileament is a self-hosted catalog for a personal 3D printing model library. It runs as one Go binary in one container with one persistent `/data` volume.

## Features

- Owner-only administration with Argon2id password hashing and SQLite-backed sessions.
- Upload STL, OBJ, 3MF, or ZIP bundles from the web UI.
- Secure ZIP extraction with junk-file filtering, path traversal rejection, and upload size caps.
- SQLite catalog with FTS search, tags, cursor pagination, collections, and durable `model.json` sidecars.
- Background thumbnail jobs with a software JPEG rasterizer and SSE thumbnail completion events.
- Embedded React/Vite/TypeScript UI with dark mode, lazy thumbnail loading, detail views, downloads, and a Three.js viewer with a 50 MB auto-load gate.
- Read-only public share links for models or collections with expiry, revoke, hit counts, `noindex`, and token-scoped asset routes.

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

## Development

The host environment for this repo does not need Go installed. Run Go commands through Docker:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23-alpine go test ./...
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
