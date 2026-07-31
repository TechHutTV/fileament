# Fileament AI Agent Guide

This file applies to the entire repository. It is the project-specific operating guide for AI coding agents. Read it before modifying code, tests, build files, documentation, or release automation.

## Mission

Fileament is a lean, self-hosted catalog for a personal 3D-printing model library. It is licensed under AGPL-3.0-only and distributed as a public multi-architecture container. The product should stay easy to deploy and operate:

- one standalone Go executable in the production image
- one persistent `/data` volume
- owner-only administration
- explicit, token-scoped public sharing
- no generated frontend bundles committed to Git
- durable model and collection sidecars so the SQLite query index can be rebuilt

Prefer simple, readable changes over framework additions or speculative abstractions.

## Non-negotiable rules

1. **Inspect before editing.** Trace definitions, call sites, tests, routes, and neighboring conventions. Do not invent symbols, dependencies, or APIs.
2. **Keep changes scoped.** Do not perform drive-by refactors, dependency upgrades, formatting sweeps, or UI redesigns.
3. **Do not commit generated frontend output.** These paths must remain untracked:
   - `web/dist/`
   - `cmd/fileament/dist/`
   - legacy `internal/server/dist/`
4. **Preserve the standalone release binary.** Production builds generate the UI, stage it only inside the builder, and compile with `-tags embedded_ui`.
5. **Preserve data durability.** Mutations affecting models or collections must keep SQLite and durable JSON sidecars consistent.
6. **Preserve security boundaries.** Owner routes and assets require authentication; public access is limited to valid share tokens and their exact scope.
7. **Treat all uploaded names and paths as hostile.** Use the existing containment and upload-cap helpers. Never concatenate an untrusted path directly beneath `/data`.
8. **Do not touch deployment data, local `.env` files, credentials, releases, tags, containers, or GHCR without explicit authorization.**
9. **Do not commit, push, merge, tag, or publish unless the user explicitly asks.**
10. **Finish with real verification.** Run the relevant tests, lint, typecheck, and build. Do not claim success from inspection alone.

## Repository map

```text
cmd/fileament/
  main.go                 Process lifecycle and HTTP server startup.
  web_external.go         Untagged local/dev web filesystem provider.
  web_embedded.go         `embedded_ui` provider for standalone releases.
  dist/                   Ignored temporary embed staging directory.

internal/config/          Environment-backed runtime configuration.
internal/server/          HTTP API, auth, uploads, SQLite, sidecars, shares,
                          background thumbnail jobs, SSE, and SPA serving.
internal/storage/         `/data` layout initialization and staging cleanup.
internal/mesh/            STL, OBJ, and 3MF parsing plus geometry statistics.
internal/render/          Software PNG thumbnail renderer.
internal/ids/             Lexically sortable ULID generation.

web/src/App.tsx           Main React application, screens, API helper, and routing.
web/src/Viewer.tsx        Lazy Three.js model viewer.
web/src/viewerGeometry.ts Viewer geometry helpers.
web/src/viewerPreferences.ts
                          Browser-persisted viewer preferences.
web/src/styles.css        Global design system and responsive styling.
web/src/*.test.tsx        Vitest/Testing Library frontend coverage.

.github/workflows/ci.yml      Pull request and push validation.
.github/workflows/release.yml Tag-triggered GHCR and GitHub release publication.
Dockerfile                    Canonical production build.
compose.yaml                  Development example, not the live deployment stack.
README.md                     User-facing setup, configuration, API, and storage docs.
LICENSE                       Verbatim GNU Affero General Public License v3 text.
```

The route-registration functions are the API source of truth:

- `internal/server/server.go`: global auth and health routes
- `internal/server/models.go`: model, upload, file, image, and tag routes
- `internal/server/collections.go`: collections, shares, and public routes
- `internal/server/thumbs.go`: thumbnail and SSE routes

The README API summary may lag a newly added route. Update it when API behavior changes.

## Runtime architecture

### Startup sequence

`server.New` receives `config.Config` and an injected `fs.FS`. It then:

1. validates that the web filesystem contains `index.html`
2. initializes the `/data` layout
3. opens SQLite with foreign keys enabled and a busy timeout
4. applies schema migrations
5. optionally seeds the owner password
6. rebuilds models from `model.json` sidecars
7. rebuilds collections from `collections.json`
8. refreshes the thumbnail render version and queues required work
9. recovers interrupted thumbnail jobs
10. starts configured background workers

Changing this order can affect recovery and durability. Add focused startup tests when changing it.

### Frontend filesystem modes

The server is deliberately unaware of whether the UI is embedded or external. It accepts an `fs.FS`.

- **Untagged development build:** `cmd/fileament/web_external.go` uses `FILEAMENT_WEB_DIR`, defaulting to `web/dist`.
- **Production release build:** `cmd/fileament/web_embedded.go` is selected by `embedded_ui` and embeds `cmd/fileament/dist`.
- **Docker:** Vite builds `/web/dist`; the Go builder copies it to `cmd/fileament/dist`; Go compiles with `-tags embedded_ui`; the distroless final image copies only `/fileament`.

A clean checkout must compile and run Go tests without either generated directory. A production Docker image must serve the UI even if `FILEAMENT_WEB_DIR` points to a nonexistent path.

Do not move `go:embed` back into `internal/server`, track a placeholder bundle, or make ordinary Go tests depend on Node output.

### HTTP and SPA behavior

Chi serves API and asset routes first, followed by the SPA fallback. Unknown browser routes receive `index.html`. `/s/*` responses must retain `X-Robots-Tag: noindex`.

The frontend uses same-origin requests and cookies. There is no separate CORS-enabled frontend architecture.

## Persistent data contract

`/data` is the only persistent application root:

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

Important invariants:

- SQLite is the searchable/query index, not the only durable record.
- Every model directory has a durable `model.json` sidecar.
- Collection metadata, membership, cover, and ordering are mirrored to `collections.json`.
- Startup can rebuild a fresh SQLite database from these sidecars.
- `/data/tmp` is disposable staging and is cleared at startup by `storage.EnsureLayout`.
- Application code must write runtime files only under `Config.DataDir`; the production container may be read-only elsewhere.
- Stored relative paths use slash-normalized values. Resolve them with `containedPath` or `containedName` before filesystem access.
- Model/file deletion must remove database rows, sidecar references, jobs, and owned files consistently.
- Upload failures must not leave partially visible models.

When changing persistence:

1. update the database transaction
2. update the relevant sidecar writer
3. verify startup reconstruction
4. add a restart/rebuild regression test

## SQLite migrations

The schema currently uses `PRAGMA user_version`. Never change only the base `schema` constant when existing installations need a migration.

For a schema change:

1. preserve upgrades from every supported older `user_version`
2. apply incremental SQL inside the migration transaction
3. increment `user_version`
4. continue rejecting databases newer than the running binary
5. test a new database and an upgrade fixture
6. test sidecar reconstruction when the changed data is durable

SQLite is `modernc.org/sqlite`; production builds use `CGO_ENABLED=0`. Do not introduce a CGO-only database dependency.

## Upload and filesystem security

Upload code is security-sensitive. Preserve these properties:

- Multipart bodies are streamed into `/data/tmp`; do not use unbounded memory buffering.
- Request limits include multipart headers and overhead, not only file payload bytes.
- ZIP extraction rejects traversal, absolute paths, junk entries, and oversized expanded content.
- Compressed and uncompressed limits remain enforced.
- Supported model formats are STL, OBJ, and 3MF; ZIP is a bundle transport.
- Grouped uploads accept loose mesh variants and must fail without a partial model if any variant is invalid.
- `containedPath` and `containedName` protect every path derived from user or sidecar input.
- Owner assets under `/files`, `/mesh`, `/thumbs`, and `/images` remain authenticated.
- Public asset handlers must confirm that the token permits the exact model, collection member, file, image, or thumbnail.

Any change to upload, ZIP, path, delete, or public-asset behavior needs adversarial tests, not only a happy-path test.

## Authentication and sharing invariants

- Owner passwords require at least 12 characters.
- Passwords use Argon2id with the existing parameters and random salts.
- Session tokens and share tokens come from cryptographically secure randomness.
- Session cookies remain `HttpOnly`, `SameSite=Lax`, and `Secure` when TLS, `X-Forwarded-Proto: https`, or an HTTPS `FILEAMENT_BASE_URL` indicates HTTPS.
- Owner-only routes use `requireAuth`.
- Public routes are explicit and token-scoped; do not create anonymous aliases to owner routes.
- Expired or revoked shares remain unavailable.
- Public share pages and responses remain `noindex`.
- API errors use the existing JSON shape: `{"error":"..."}`.

Never log passwords, session tokens, share tokens, hashes, or credential-bearing environment values.

## Thumbnail and background-job invariants

Thumbnail work is persisted in the `jobs` table and processed by configured workers.

- Tests commonly use `ThumbWorkers: 0` for deterministic manual processing.
- Jobs transition through pending/running states and interrupted running jobs return to pending at startup.
- `thumbnailRenderVersion` deliberately requeues existing files after renderer changes. Increment it only when all existing thumbnails should be regenerated.
- Preserve the selected card thumbnail behavior when regenerating files.
- Thumbnail completion is published through authenticated SSE at `/api/events`.
- Coordinate worker shutdown with `App.Close`; do not leak goroutines.

## Backend conventions

- Go version: 1.23; module toolchain: Go 1.23.12.
- Prefer the standard library and current dependencies. New dependencies need a concrete benefit.
- Keep HTTP handlers thin enough to test through `httptest`, but do not create abstraction layers without repeated need.
- Use Chi route groups and middleware consistently.
- JSON fields are camelCase and defined on Go structs.
- IDs use `internal/ids.New` ULIDs.
- Timestamps are Unix seconds.
- Use transactions when several database writes form one operation.
- Check `rows.Err`, close rows, and roll back failed transactions.
- Preserve stable sorting and cursor tie-breakers when changing catalog queries.
- Run `gofmt` on every changed Go file.
- Tests are white-box package tests under the package being exercised; reuse helpers in `internal/server/server_test.go` and `upload_test.go`.

## Frontend conventions

- React 19, strict TypeScript, Vite, TanStack Query, Three.js, Vitest, and Testing Library.
- `App.tsx` currently owns application routing and most screens. Follow its existing patterns unless a change clearly justifies extracting a cohesive module.
- Navigation uses `history.pushState` plus the `fileament:navigate` event, not React Router.
- Use the shared `api` helper for same-origin JSON/FormData requests and included credentials.
- Preserve query keys and invalidate affected data after mutations.
- Keep the Three.js viewer lazy-loaded. Preserve the 50 MB automatic viewer-load gate unless product requirements change.
- Validate browser-persisted preferences before using them and tolerate unavailable `localStorage`.
- Extend existing CSS tokens, surfaces, spacing, responsive breakpoints, and dark-mode behavior in `styles.css`; avoid introducing a second styling system.
- Maintain keyboard focus states, semantic controls, useful empty/loading/error states, and mobile layouts.
- Add or update Testing Library tests for visible behavior. Do not test implementation details.

## Development setup

### Initial inspection

Before work:

```sh
git status --short --branch
git log -3 --oneline
```

Respect existing modifications. Never discard or overwrite unrelated work.

### Frontend

Run from `web/`:

```sh
npm ci
npm test
npm run lint
npm run typecheck
npm run build
```

`npm run build` writes ignored output to `web/dist`.

### Go

The maintainer host may not have Go installed. Use the repository’s CI-equivalent container command from the repository root:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.23-alpine \
  sh -lc 'export PATH="/usr/local/go/bin:$PATH"; go test $(go list ./... | grep -v "/node_modules/"); go vet $(go list ./... | grep -v "/node_modules/")'
```

For focused iteration, replace the package expression with a package and `-run` test selector. Run the full command before completion.

### Production build

```sh
docker build -t fileament:dev .
```

The final image is distroless and has no shell. Do not add shell-based in-container health checks. Verify externally through `/healthz`, `/`, and at least one hashed JavaScript asset.

### Generated-asset guard

Before a commit:

```sh
test -z "$(git ls-files web/dist cmd/fileament/dist internal/server/dist)"
git diff --check
```

After verification, remove generated `web/dist` and `cmd/fileament/dist` output when practical. Never stage it with `git add -f`.

## Test matrix by change type

| Change | Minimum verification |
| --- | --- |
| Go handler, auth, database, uploads | Focused test, full Go tests, Go vet |
| Mesh parser | `internal/mesh` normal and adversarial tests, full Go tests |
| Thumbnail renderer/jobs | `internal/render` and server thumbnail tests, full Go tests |
| React behavior | Relevant Vitest test, full frontend tests, lint, typecheck |
| CSS/layout | Frontend tests, lint, typecheck, production build, visual check at desktop and mobile sizes |
| API contract | Backend `httptest` coverage and matching frontend tests/types |
| Schema or sidecars | New DB, upgrade, mutation durability, and startup reconstruction tests |
| Docker/embed/build tags | Clean untagged Go build, frontend build, Docker build, image/runtime smoke test |
| Release workflow | YAML validation, Docker build, then post-tag GHCR manifest and GitHub release verification |

For regressions, write a test that fails for the reported behavior before implementing the fix when feasible.

## Git and pull-request workflow

- Start from a clean, synced `main`.
- Use focused branches such as `feat/...`, `fix/...`, `refactor/...`, or `chore/...`.
- Existing commit style is Conventional Commit-like: `feat:`, `fix:`, `refactor:`, `test:`.
- Keep one logical change per commit when practical.
- Before committing, review the staged diff, run `git diff --cached --check`, and confirm no generated assets are tracked.
- Do not rewrite shared history.
- Do not merge a PR unless explicitly asked.
- CI runs on both pushes and pull requests, so two equivalent validation checks may appear.

## Releases

Stable releases are tag-driven:

1. start from clean, synced `main`
2. choose a SemVer tag in the form `vX.Y.Z`
3. for `vX.Y.0`, copy `.github/release-notes/TEMPLATE.md` to `.github/release-notes/vX.Y.0.md`, complete the detailed notes, and run the documented validation command
4. for `vX.Y.Z` where `Z > 0`, preview GitHub's generated notes; the workflow will publish the concise patch format automatically
5. create and push the tag only with explicit publication approval
6. `.github/workflows/release.yml` validates the release-note policy, then builds `linux/amd64` and `linux/arm64`
7. GHCR receives version, major/minor, major, and `latest` tags
8. the workflow publishes provenance, an SBOM, and the standardized GitHub release

After tagging, do not report success until all of these are verified:

- release workflow completed successfully
- GitHub release exists for the exact tag
- `ghcr.io/techhuttv/fileament:X.Y.Z` resolves
- the manifest contains both `linux/amd64` and `linux/arm64`
- `latest` points to the new stable release when intended

A release is an external publication. Never infer approval merely from code being merged.

## Licensing

- Fileament is licensed under `AGPL-3.0-only`.
- Keep `LICENSE` as the unmodified GNU Affero General Public License version 3 text.
- Do not relicense the project or add an "or later" option without explicit owner approval.
- Check new dependencies, copied code, fonts, icons, and other bundled assets for AGPL-compatible terms and required attribution before adding them.
- Preserve third-party notices and license files when their terms require it.
- Keep the README license section accurate and linked to `LICENSE`.

## Deployment boundary

`compose.yaml` in this repository is a development example. It is not necessarily the maintainer’s live stack.

On Brandon’s host, the persistent local deployment is managed separately under `~/docker/fileament`. Do not rebuild, restart, edit, migrate, or inspect its secret-bearing `.env` unless explicitly asked. Preserve its private bind address, bind-mounted data, read-only filesystem, dropped capabilities, non-root UID/GID, and restart policy when deployment work is authorized.

Never treat a successful repository Docker build as proof that the live deployment was updated.

## Documentation expectations

Update `README.md` when changing:

- user-visible behavior
- configuration variables or defaults
- API routes or request/response contracts
- deployment or build commands
- persistent storage layout
- supported model formats
- release usage
- license or attribution requirements

Document the current behavior, not implementation history. Keep commands tested and in execution order.

## Definition of done

Before saying a task is complete, confirm:

- [ ] Every requested behavior is implemented.
- [ ] The change follows existing package and UI conventions.
- [ ] Security and data-durability invariants are preserved.
- [ ] Focused regression tests exist where appropriate.
- [ ] Full relevant Go and/or frontend gates pass.
- [ ] Docker is built and smoke-tested when production packaging changed.
- [ ] `git diff --check` passes.
- [ ] No generated frontend files, databases, credentials, caches, or deployment data are tracked.
- [ ] New dependencies and bundled assets have compatible licenses and required attribution.
- [ ] README/API documentation is updated when user-facing behavior changed.
- [ ] The worktree contains only intentional changes.
- [ ] No commit, push, deployment, tag, or release occurred without explicit approval.
