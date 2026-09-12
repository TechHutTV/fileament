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
| `FILEAMENT_OWNER_PASSWORD` | unset | Seeds the owner password on first boot using the same policy as setup. Ignored after an owner password exists. |
| `FILEAMENT_MAX_UPLOAD_MB` | `2048` | Maximum upload request size and maximum expanded ZIP size in MiB. |
| `FILEAMENT_MAX_BACKUP_MB` | `8192` | Maximum uploaded backup size and maximum expanded backup size in MiB. |
| `FILEAMENT_THUMB_WORKERS` | `2` | Number of background thumbnail workers. |
| `FILEAMENT_BASE_URL` | unset | Public HTTP(S) origin used when displaying share links and determining secure-cookie behavior. Paths are ignored because Fileament does not support subpath mounting. |

Example:

```yaml
environment:
  FILEAMENT_BASE_URL: https://models.example.com
  FILEAMENT_MAX_UPLOAD_MB: "4096"
  FILEAMENT_MAX_BACKUP_MB: "16384"
  FILEAMENT_THUMB_WORKERS: "4"
```

`FILEAMENT_WEB_DIR` is only used by untagged development builds. Official production images embed the web interface and do not need a separate static directory.

Authentication endpoints require JSON, limit request bodies to 16 KiB and passwords to 1024 bytes, and allow five seconds to receive the body. At most two authentication requests run concurrently. A shared installation-wide limit allows an initial burst of 10 attempts and replenishes one attempt every six seconds; excess attempts return HTTP `429` with `Retry-After`. Forwarded IP headers do not bypass this limit. Idle HTTP connections close after 60 seconds; active uploads, downloads, and event streams are unaffected by that idle timeout.

New owner passwords must contain at least 12 characters, counted as Unicode code points, and at most 1024 UTF-8 bytes. This applies to setup, password changes, and the first-boot environment seed. Invalid first-boot seeds stop startup with a diagnostic that does not include the password.

Changing the password revokes every existing session and keeps the current browser signed in with a new cookie. Other browsers must sign in again. Password changes and logout close existing event streams, which reconnect only with a valid session. Streams also recheck authorization before sending events and once per minute while idle. Expired sessions are pruned at startup and at most once per hour during authenticated traffic.

## Using Fileament

### Upload models

The upload screen supports loose STL, OBJ, and 3MF files plus ZIP archives. Choose **Separate models** to create one model per loose file or **One model with variants** to keep several meshes under one model. File selection stays disabled until one of these organization modes is selected. Grouped uploads list every variant while the upload runs, then show each generated thumbnail, filename, format, and size. Unwanted persisted variants can be removed individually after confirmation.

ZIP processing rejects unsafe paths and common junk files. Uploads are streamed into the persistent data volume instead of being buffered in memory.

ZIP mesh and image names are flattened into their respective directories. Duplicate names, including case variants and existing numeric suffixes, receive an available numeric suffix. Each entry gets its own file, so deleting one variant cannot remove another variant's contents.

Mesh processing has separate limits from the upload request size. Each mesh may occupy at most 512 MiB on disk and produce at most 1,000,000 triangles. OBJ and 3MF inputs may contain at most 2,000,000 vertices; STL and OBJ text lines are limited to 1 MiB. Coordinates and transforms must be finite, and final coordinate magnitudes cannot exceed 10¹² model units (millimeters after 3MF unit conversion).

A 3MF package may contain at most 2,048 ZIP entries and 64 MiB of expanded content, including attachments. ZIP metadata reads have a 4 MiB budget. XML and component nesting are limited to 64 levels, and both resource declarations and expanded component visits have a 100,000-item budget. Package validation checks actual expanded bytes before decoding model parts serially. These limits also apply when thumbnail workers read existing files.

At most two meshes are parsed concurrently, with a 30-second deadline including admission time. Thumbnail jobs have a one-minute deadline including admission, with a separate 30-second render deadline and at most two active jobs regardless of the configured worker count. Upload parsing stops when its request is canceled. Shutdown cancels active thumbnail processing and returns interrupted jobs to pending; invalid or timed-out jobs are marked failed. Rendering preserves an existing thumbnail if the replacement fails.

### Organize your library

Use tags for flexible filtering and collections for curated groups. Collections retain their own ordering, descriptions, and cover models.

Tags with the same normalized slug share the existing label. Reusing a tag does not rename it on other models.

Deleting the last variant keeps the model's metadata, images, and collection membership. Empty models remain visible in the catalog and share pages; the owner can add new variants or delete the model. Model API responses always include a `files` array, which is empty when there are no variants.

### Preview and download

Supported meshes can be opened in the browser viewer. Files larger than 50 MiB, files with more than 250,000 triangles, and files without valid geometry statistics wait for **Load 3D view** before loading. This applies to owner and public share pages; each variant requires its own confirmation. Original files remain available for download, and each variation can be renamed inline without changing its file format.

Raw mesh endpoints serve original bytes as `application/octet-stream` with content sniffing disabled and a sandbox policy. Model files are never served as HTML; download endpoints retain their attachment filenames.

If a 3D view fails to load, its error stays inside the preview so downloads and navigation remain available. Browser storage is optional: login and theme controls continue working when saved preferences cannot be read or written, with theme changes lasting for the current page session.

### Share models and collections

Share links are read-only and scoped to one model or collection. They can expire after a chosen number of days or remain active without an expiration date, and they can be revoked at any time. **Settings → Share links** shows each target, creation and expiration dates, lifecycle status, copy-ready public URL, and shared-page view count. A view is one successful shared-page load; thumbnail, mesh, image, file, and background access-status requests do not inflate it. Open shared pages periodically revalidate access and stop displaying cached content after expiration or revocation. Shared pages are marked `noindex` for search engines.

## Data and backups

Everything required to restore Fileament lives under `/data`:

```text
/data/
  fileament.db
  collections.json
  tmp/
  backups/
  .restore/
  .mutations/
  models/
    <model-id>/
      model.json
      files/
      images/
      thumbs/
```

Model and collection changes publish in sequence so simultaneous requests preserve each other's changes. Upload parsing and thumbnail rendering run outside that publication lock. Before changing active data, Fileament saves a rollback snapshot and syncs a journal under `/data/.mutations`. Snapshots use hardlinks for unchanged files, with a copy fallback on filesystems that do not support hardlinks. Sidecar and thumbnail replacements use unique temporary files; success is returned after the resulting files, sidecars, and commit marker have been synced.

Failed changes restore the preceding files and sidecars and reconcile SQLite before returning the error. Startup rolls back interrupted changes and retains completed commits. If commit finalization or rollback cannot finish, Fileament remains in maintenance, returns HTTP `503`, and retries journal recovery after restart. Resolve the underlying storage problem, restart Fileament, and check the resulting state before retrying the change. Keep `.mutations` intact while recovery is pending. Its workspace is excluded from application-created backups and cannot be supplied by a restore archive.

Sidecars define model and collection contents during reconstruction: stale indexed files, images, models, collections, and unused tags are removed from SQLite. An existing model directory without a valid sidecar stops startup so its remaining data and index can be recovered; original files are not silently discarded. Unreferenced files on disk are preserved. Thumbnail files are derived data and may be absent while previews are being regenerated.

Deleting a file or model also removes its thumbnail jobs. A render already in progress cannot recreate deleted model data. Completed and failed job history is capped at 1,000 records; records older than seven days are removed at startup and as jobs finish. Pending and running jobs are preserved, and interrupted work resumes after restart.

Use **Settings → Backup and restore** to create a versioned `.fileament` backup. It contains a consistent SQLite snapshot plus every persistent data file, including models, images, collections, settings, the owner password hash, and share links. Login sessions and transient backup, restore, and mutation workspace are intentionally excluded. Treat the downloaded file as sensitive.

Restoring is a full replacement, not a merge. Fileament validates the uploaded archive, database, sidecars, checksums, paths, sizes, and format versions before showing its contents for confirmation. Only one reviewed backup is staged at a time; reviewing another replaces it, restore tokens expire after one hour, and abandoned restore workspace is cleared at startup. Applying it creates a pre-restore safety backup under `/data/backups`, pauses writes and thumbnail workers, swaps the validated data, reopens and migrates SQLite, and automatically rolls back if activation fails. If both activation and the immediate rollback fail, Fileament retries journal recovery once, remains in maintenance with an unhealthy `/healthz` response if recovery is still impossible, and retries recovery at the next startup. Every login session is invalidated after a successful restore; sign in with the owner password stored in that backup. Safety backups are not recursively included in later downloads and can be removed manually after the restored installation has been verified.

The owner-only backup API uses these endpoints:

- `POST /api/backups` creates and downloads a backup.
- `POST /api/backups/inspect` accepts one multipart `file` and returns a validated manifest plus a temporary restore token.
- `POST /api/backups/restore` accepts that token and the exact confirmation value `RESTORE`.

For an independent infrastructure-level backup, copy the entire volume, not only `fileament.db`. The JSON sidecars preserve model and collection metadata and can rebuild a fresh SQLite index at startup. Stop Fileament first so SQLite and normal files share one consistency boundary:

```sh
docker stop fileament
# Back up the fileament-data volume with your normal Docker volume backup tool.
docker start fileament
```

Restore and startup reconstruction reject invalid model metadata: model, file, and image IDs must be canonical ULIDs; assets must belong to their model and use the expected `files/`, `images/`, and `thumbs/` paths. Existing per-file JPEG thumbnails remain supported for migration to PNG. Do not rename IDs or edit stored paths by hand.

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

Browser mutations must originate from Fileament itself. Go's origin protection rejects cross-origin unsafe requests, including requests from sibling subdomains, using `Sec-Fetch-Site` or an `Origin`/`Host` comparison for older browsers. Preserve the original `Host` header at the reverse proxy. Command-line clients without browser origin headers remain supported. JSON mutation routes require `Content-Type: application/json`; upload routes continue to accept multipart form data. Rejections return a JSON error with HTTP `403` for cross-origin requests or `415` for an incorrect JSON media type.

API responses, owner assets, public shared assets and share pages use `Cache-Control: private, no-store`, including errors and access-status responses. Configure proxies to honor this policy so cached content cannot bypass a later authorization or share-revocation check. Revocation cannot retract a copy someone already downloaded.

Owner and share pages reject framing and send no referrer information on outgoing requests. The content security policy also blocks plugins, foreign base URLs, and cross-origin form targets. It leaves script, style, image, and connection sources unrestricted so the existing viewer and Markdown image behavior remain supported. Raw mesh responses retain their stricter sandbox policy.

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

See [performance measurements](docs/performance.md) for the reproducible catalog benchmarks and index storage/write tradeoffs.

Fileament uses Go 1.26.8 or newer from a supported release line for the backend and React, TypeScript, and Vite for the frontend. See [`AGENTS.md`](AGENTS.md) for the complete architecture, development workflow, security invariants, and verification requirements.

Frontend checks use Node 22.12 or newer and run from `web/`:

```sh
npm ci
npm audit --audit-level=low
npm test
npm run lint
npm run typecheck
npm run build
```

Go checks can run without installing Go on the host:

```sh
docker run --rm -e GOTOOLCHAIN=local -v "$PWD":/src -w /src golang:1.26.8-alpine \
  sh -lc 'export PATH="/usr/local/go/bin:$PATH"; go test $(go list ./... | grep -v "/node_modules/"); go vet $(go list ./... | grep -v "/node_modules/")'
```

Generated frontend output is built when needed and must not be committed.

CI also scans the backend with `govulncheck` v1.8.0. Keep the minimum Go version in `go.mod`, the production builder, and the CI images on a supported patch release from the [Go release history](https://go.dev/doc/devel/release). Builds with older toolchains are rejected rather than silently producing an executable with known standard-library vulnerabilities.

## License

Fileament is licensed under the [GNU Affero General Public License v3.0](LICENSE). See `LICENSE` for the complete terms.
