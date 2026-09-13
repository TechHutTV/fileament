# Performance measurements

## Catalog indexes

Schema version 3 adds ordered indexes for the four catalog sorts, model files and images, collection membership, pending jobs, job/file cleanup, tag membership, and session expiry. Migration tests cover fresh databases and versions 1 and 2. Query-plan tests verify index use and the absence of temporary ordering tables for these query shapes. Pending jobs use their ID to break creation-time ties.

Run the synthetic benchmark from the repository root with Go 1.26.8 or newer:

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkCatalogIndexes$' -benchtime=100ms -count=1 -v
```

The baseline uses schema version 2; the comparison applies the index migration. Each model has a description, three file records and one image record. Both databases contain 999 thumbnail jobs, including 30 pending jobs. Mesh payloads are not written. The benchmark logs database size and one bulk transaction's insertion time, then repeatedly executes the catalog's first-page ID selection and relationship/worker queries. It excludes authentication, model hydration, HTTP serialization, disk payload parsing, and frontend rendering.

Local results on September 12, 2026, with Go 1.26.8 on macOS arm64:

| Models | Created-order query, baseline → indexed | Database bytes, baseline → indexed | Bulk insertion, baseline → indexed |
| --- | --- | --- | --- |
| 1,000 | 0.390 ms → 0.0156 ms | 1,064,960 → 1,400,832 | 48.5 ms → 52.0 ms |
| 10,000 | 4.57 ms → 0.0156 ms | 8,343,552 → 11,014,144 | 378 ms → 599 ms |
| 100,000 | 48.3 ms → 0.0160 ms | 82,235,392 → 109,395,968 | 3.93 s → 6.64 s |

At 100,000 models, the other first-page sorts took 31.6–47.1 ms before indexing and 0.0149–0.0160 ms afterward. A model's file-ID query fell from 14.1 ms to 0.0073 ms; its image-ID query fell from 4.07 ms to 0.0066 ms. Selecting a pending job from 999 records fell from 0.068 ms to 0.0080 ms.

These are short, repeated local database-query measurements with warm caches, not production latency guarantees. The indexes increased this fixture's database size by 33% and bulk insertion time by 69% at 100,000 models. Actual uploads also hash and parse meshes, so this bulk SQL fixture does not measure upload throughput. The read improvement justifies the indexes for browsing a growing library; rerun the benchmark on representative hardware and data when changing query shapes or adding indexes. Filter combinations and deep pagination still need separate measurements.

## Catalog summaries and collection pages

Catalog cards use one SQL query, including the next-page cursor values and the first variant's format/triangle count. They omit descriptions, images, tags, checksums, and other variant details. The former 24-card path performed 97 model-data queries, or 101 with a next-page cursor. Tag and collection filters use membership subqueries, which cannot multiply a model row; FTS joins use its unique row ID. A regression covers combined search, tag, collection, and all sorts, including an ID/slug collision that matches two collections. Query-plan tests check ordering and membership indexes.

Run the card benchmark with:

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkCatalogCards$' -benchtime=30x -count=1
```

Both paths use the indexed catalog fixture above, with synthetic 64-character file checksums. The baseline reproduces the former ID query plus full model hydration and cursor lookup. The comparison runs the summary query. Both serialize a 24-card response. Results from September 12, 2026, on Apple M5/macOS arm64 with Go 1.26.8:

| Models | Full hydration → summaries | JSON bytes, full → summary | Allocated bytes/request, full → summary |
| --- | --- | --- | --- |
| 1,000 | 1.57 ms → 0.100 ms | 31,890 → 3,402 | 270,716 → 20,127 |
| 10,000 | 1.48 ms → 0.106 ms | 31,963 → 3,475 | 271,892 → 20,967 |
| 100,000 | 1.23 ms → 0.100 ms | 32,037 → 3,549 | 269,160 → 21,306 |

At 100,000 models the measured path is about 12 times faster, with 89% less JSON and 92% fewer allocated bytes. These are short warm-cache measurements that include SQL, Go object construction, and JSON serialization. They exclude authentication, network transfer, file payloads, and browser rendering. They do not establish production latency or filtered/deep-page throughput.

Collection detail uses three queries for metadata, member count, and one page of cards, instead of hydrating every member. Pages default to 24 and are capped at 100. Public collection responses add only one complete selected model, with current token and membership checks. The owner collection list gets cover/count/membership summaries in one query, without transferring every member ID. Membership and share creation use existence/name queries rather than full model hydration.

Collection reordering still reads member IDs and persists the complete order, and mutations still publish the complete durable collection sidecar. Those writes remain proportional to membership size. Snapshot/fsync cost and concurrent write throughput require separate measurements; the card benchmark does not measure them. Pagination tests cover tied positions, invalid/foreign cursors, cross-page moves, a single-connection pool, cancellation, backups, fresh-index reconstruction, and removal/revocation of public access.

## SQLite concurrency

The runtime uses four open/idle connections, WAL, `synchronous=FULL`, a five-second busy timeout, and immediate write transactions. Immediate transactions reserve the single SQLite writer before reading data to update, avoiding deferred read-to-write upgrade failures. Read-only transactions remain deferred. Each connection enables foreign keys; automatic WAL checkpoints retain SQLite's 1,000-page default. Model/collection publication still uses the existing mutation locks and recovery journal.

The driver is `modernc.org/sqlite` 1.46.2, with SQLite 3.51.3. This includes the upstream [WAL-reset corruption fix](https://sqlite.org/releaselog/3_51_3.html); enabling WAL on the earlier bundled SQLite would be unsafe. WAL requires local filesystem shared-memory support. Full synchronization retains commit durability; it is not reduced to `NORMAL` to improve benchmark results.

```sh
go test ./internal/server -run '^$' -bench '^BenchmarkDatabaseConcurrency$' -benchtime=1000x -count=3 -cpu=8
```

The 1,000-model fixture above runs eight concurrent goroutines. Six of each eight operations read a 25-card page; one inserts a model/file/job transaction; one claims and completes a thumbnail job. It exercises the application's claim/completion queries and publication locks. It excludes payload parsing/rendering, sidecar snapshots, HTTP, and filesystem publication, so it measures database contention rather than end-to-end import throughput. Every configuration uses the patched driver and full synchronization.

Median results from three runs on Apple M5/macOS arm64, Go 1.26.8, September 12, 2026:

| Journal / maximum connections | Mixed operation time | Average read latency within median run | Failed operations per 1,000, range |
| --- | --- | --- | --- |
| DELETE / unlimited, deferred writes | 0.500 ms | 2.515 ms | 2–29 |
| DELETE / 4, immediate writes | 0.403 ms | 1.081 ms | 0 |
| WAL / 1, immediate writes | 0.114 ms | 0.468 ms | 0 |
| WAL / 2, immediate writes | 0.118 ms | 0.331 ms | 0 |
| WAL / 4, immediate writes | 0.148 ms | 0.246 ms | 0 |
| WAL / 8, immediate writes | 0.174 ms | 0.119 ms | 0 |

Four connections balance browsing latency, write throughput, and per-connection memory. A single connection has lower mixed-operation overhead in this short fixture but serializes readers behind writes. Eight improves reads further while increasing total time and connection cost. The unlimited baseline includes failed operations and cannot be treated as successful throughput. Rerun on the deployment's storage before drawing production latency conclusions.

Request reads, authorization checks, direct handler transactions, and worker SQL use cancellation contexts. Recovery/finalization remains independent of a disconnected request so files and sidecars can still be made consistent. Restore closes and checkpoints the active database before recording the files to swap; rollback also removes journal files created by the replacement database. Tests cover concurrent read/modify/write transactions, reader snapshots during writes, pool saturation, expensive query cancellation and connection reuse, backup/restore, and recovery from an interrupted replacement with live WAL data.

## Import inspection and startup

Ingestion uses the same bounded parsers as rendering but accumulates triangle counts and bounds without retaining expanded triangles. STL and OBJ feed SHA-256 during parsing; the random-access 3MF decoder retains its bounded package model and uses a separate sequential checksum pass. OBJ keeps the vertex table needed for indexed faces. Workers still parse persisted files for rendering; no cross-request geometry cache is introduced.

```sh
GOTOOLCHAIN=go1.26.8 go test ./internal/server -run '^$' -bench 'Benchmark(MeshIngestion|SidecarStartup)' -benchtime=3x -count=1
```

The ingestion fixtures contain 100,000 repeated planar triangles. Measurements include file metadata, checksum, validation, and statistics, excluding upload transport, publication, and rendering. Results below are means of three iterations on Apple M5/macOS arm64, Go 1.26.8, September 12, 2026. The baseline uses commit `78b10d4`; the comparison includes the streaming inspection and startup changes. These short, synthetic measurements use warm local files and are not production latency estimates. Allocated bytes are cumulative allocations per operation, not peak resident memory.

| Fixture | Baseline time | Inspection time | Baseline allocated bytes | Inspection allocated bytes |
| --- | ---: | ---: | ---: | ---: |
| Binary STL | 7.28 ms | 5.41 ms | 7,301,498 | 67,384 |
| ASCII STL | 42.47 ms | 24.21 ms | 75,669,941 | 24,133,109 |
| OBJ | 21.67 ms | 8.14 ms | 52,400,752 | 7,267,496 |

Startup fixtures have two small STL files and three shared tags per model, one collection per 100 models, and persistent pending thumbnail jobs. Setup writes real files and sidecars and primes SQLite before measurement. Each operation opens the database, runs the full application initialization with workers disabled, reads the connection's SQLite change count, and closes the application. It does not include fixture creation or HTTP requests.

| Models | Baseline reopen | Optimized reopen | Baseline SQLite row changes | Optimized row changes |
| ---: | ---: | ---: | ---: | ---: |
| 1,000 | 986 ms | 91 ms | 103,714 | 0 |
| 10,000 | 65,035 ms | 898 ms | 1,036,225 | 0 |

The change count includes trigger and FTS maintenance writes. Startup still reads and validates sidecars and compares full indexed model metadata and relationships. This avoids a persistent fingerprint cache and its invalidation requirements. A difference or unreadable model record takes the existing reconstruction path; missing FTS rows and thumbnail jobs are repaired. Stale model/file/image/collection relationships are still pruned, and fresh-index reconstruction is covered by restart tests. The comparison adds some serialization: total startup allocations rose from about 141 MB to 146 MB at 10,000 models, while allocation count fell from 3.07 million to 2.57 million.

The schema-version-5 migration replaces the single-column job/file index with `(file_id, type, created_at DESC, id DESC)`. The previous planner could choose the job-type index for each per-file lookup, scanning unrelated jobs repeatedly. Query-plan tests require the file-first index for reconstruction and latest-job queries; it also supports deletion's file-ID lookup. No index hint or reduced durability setting is needed.

The renderer remains unchanged. A separate 512-pixel, 20,000-triangle curved-grid benchmark averaged 20.4 ms and 4.14 MB allocated per PNG. In its three-second CPU profile, DEFLATE compression accounted for about 45% of sampled CPU time, PNG filtering 14%, and triangle filling 8%. This scene does not justify adding rasterizer complexity; heavily overlapping geometry can behave differently.

```sh
GOTOOLCHAIN=go1.26.8 go test ./internal/render -run '^$' -bench BenchmarkThumbnailGrid -benchtime=3s -cpuprofile=/tmp/fileament-render.prof -o /tmp/fileament-render.test
GOTOOLCHAIN=go1.26.8 go tool pprof -top /tmp/fileament-render.test /tmp/fileament-render.prof
```

## Backup exports

Run a small backup benchmark, or opt into a 2 GiB disk fixture:

```sh
GOTOOLCHAIN=go1.26.8 go test ./internal/server -run '^$' -bench '^BenchmarkBackupSnapshot$' -benchtime=1x -benchmem
FILEAMENT_BENCH_LARGE_BACKUP=1 GOTOOLCHAIN=go1.26.8 go test ./internal/server -run '^$' -bench '^BenchmarkBackupSnapshot$' -benchtime=1x -benchmem
GOTOOLCHAIN=go1.26.8 go test ./internal/server -run '^$' -bench '^BenchmarkBackupCompression$' -benchtime=3x
```

The large fixture writes 1,024 files of 2 MiB each beneath the model tree. A deterministic random block makes their contents difficult to compress; these are opaque backup payloads, not mesh-ingestion fixtures. The fixture uses an otherwise empty SQLite catalog. The large run needs roughly 4 GiB of free space and removes its data afterward.

A single warm local run on Apple M5, macOS/APFS, Go 1.26.8, compared the previous export at `6193eed` with snapshot capture followed by unlocked compression:

| Measurement | Previous export | Snapshot export |
| --- | --- | --- |
| Library lock held | 17.423 s | 0.212 s |
| Complete export, excluding network transfer | 17.443 s | 17.790 s |
| Archive size | 2,148,339,539 B | 2,148,339,591 B |
| Total Go allocations | 36.3 MB | 38.4 MB |

The measured additional allocated disk space at the snapshot export's peak was 2,164,498,432 B. Hardlinks reused the original payload blocks; the archive accounted for almost all new space. The snapshot also needs SQLite and directory metadata. File deletion while compression is running keeps the old blocks alive until snapshot cleanup. A filesystem without hardlinks copies those payloads, increasing both lock time and temporary disk use.

These measurements isolate export work without simultaneous clients or thumbnail renders. Capture still waits for existing data operations, scans persistent entries, snapshots SQLite, and copies files that cannot be hardlinked. Larger databases, slow disks, external mounts, and many small files can increase the pause. The allocation figures are cumulative allocations, not peak process RSS.

For a 16 MiB incompressible buffer, ZIP Deflate took 144.2 ms and ZIP Store took 1.50 ms (three iterations, excluding disk and network). Exports therefore store already-compressed 3MF, PNG, JPEG, WebP, GIF, AVIF, ZIP, and gzip files without another compression pass; text meshes, metadata, and SQLite remain compressed. Actual size savings depend on the input.

The settings page prepares a backup through a small JSON response and offers an authenticated normal download link. Archive bytes are no longer fetched into a JavaScript Blob. Backend tests cover a catalog deletion during compression with subsequent restore validation, session-bound downloads, byte ranges, expiry, replacement, cancellation, archive limits, symlink rejection, cleanup, and safety-backup retention. Frontend tests cover preparation, failure/retry, link delivery, expiry, and storage categories. Browser download-manager memory and network throughput were not benchmarked.

## Viewer rendering and resources

The viewer uses demand rendering. Drei controls request frames as the camera changes, as described in the [React Three Fiber performance guide](https://raw.githubusercontent.com/pmndrs/react-three-fiber/master/docs/advanced/scaling-performance.mdx). A headless integration test uses the installed React Three Fiber scheduler, Drei Bounds, and OrbitControls with a draw counter in place of the graphics driver. It verifies fitting, zoom, damped rotation, reset, and no additional draws during one-second idle windows after each movement settles. It also verifies that a replaced model detaches from the scene before its geometry is disposed. This tests frame scheduling and camera behavior, not GPU utilization or end-to-end frame time.

Each viewer owns its parsed object instead of retaining all variants in the global loader cache. STL normals are repaired in the owned geometry without copying its attributes. OBJ meshes use the selected material color, while 3MF materials and textures remain intact. Cleanup disposes each unique geometry, material, and texture once, including resources shared within a single 3MF object. Separate viewers parse separate objects, so closing one cannot dispose another viewer's resources.

Run CPU and retained-buffer benchmarks from web/:

~~~sh
npx vitest bench --run src/viewerResources.bench.ts
npx esbuild benchmarks/viewer-memory.mjs --bundle --platform=node --format=esm --outfile=/tmp/fileament-viewer-memory.mjs
node --expose-gc /tmp/fileament-viewer-memory.mjs
~~~

Local results on Apple M5/macOS, Node 22.23.1, Vitest 4.1.11, and the locked Three.js dependencies:

| Geometry preparation | Previous clone and normal repair | Repair owned geometry |
| --- | --- | --- |
| 100,000 triangles | 3.23 ms | 2.90 ms |
| 1,000,000 triangles | 46.23 ms | 40.70 ms |

These are means from ten iterations after two warmups on a synthetic STL-like geometry. The separate edge-generation pass took 280 ms on a 100,001-triangle fixture (three iterations, one warmup). The viewer omits that pass and shadow rendering above 100,000 triangles. Smaller meshes retain their edge outlines and shadows.

The memory fixture switches through twenty distinct 100,000-triangle variants and forces garbage collection before measuring Node's retained ArrayBuffer bytes. It compares the previous source-cache-plus-active-copy strategy with the current asset preparation and disposal functions:

| Strategy | Retained geometry buffers after twenty variants | After releasing all references |
| --- | --- | --- |
| Previous cache and active copy | 151,200,000 B | 0 B |
| Owned active asset | 7,200,000 B | 0 B |

The previous strategy's teardown explicitly clears the simulated cache; ordinary variant navigation previously kept those source buffers. These numbers exclude temporary download buffers, textures, other JavaScript heap objects, graphics-driver allocations, and peak RSS. They are not a whole-browser memory cap. Actual graphics performance and visual output still need a connected browser; none was available for local profiling. Existing import budgets and the 50 MiB/250,000-triangle automatic-viewer gates remain unchanged.

## Frontend transfer and thumbnail refreshes

The frontend build writes gzip copies of hash-named JavaScript and CSS beside the original assets. Both representations are embedded in production, so serving gzip needs no request-time compression or growing in-memory asset cache. Encoding selection handles quality values and exclusions, and responses include `Vary: Accept-Encoding` so caches distinguish representations, following the [HTTP negotiation rules](https://www.rfc-editor.org/rfc/rfc9110.html#name-accept-encoding).

An embedded-server test fetched the final build over loopback HTTP with an explicit gzip request, checked its headers and content length, and verified that decompression reproduced the original bytes. These are measured response payloads, rather than Vite's estimated gzip sizes:

| Asset | Original bytes | Gzip bytes received |
| --- | ---: | ---: |
| Main JavaScript | 432,684 | 130,944 |
| Lazy viewer JavaScript | 925,064 | 251,525 |
| CSS | 35,759 | 6,704 |
| All three | 1,393,507 | 389,173 |

For this build, gzip reduces the combined payload by 72.1%. The viewer remains lazy: ordinary navigation does not fetch it until a model is opened. These totals exclude HTTP/TLS overhead, HTML, API data, images, and model downloads. They do not measure network latency or browser rendering speed. The existing uncompressed viewer chunk-size warning remains.

The added gzip payload occupies 389,173 bytes alongside the originals in the embedded UI; executable overhead and alignment are separate. Compressing all three files and writing their gzip copies took a mean 22.8 ms over ten sequential runs with warm filesystem caches on Apple M5/macOS and Node 22.23.1 (range 21.6–24.6 ms). This cost occurs during the build. From `web/`, the measurement can be repeated with:

~~~sh
npm run build
node --input-type=module <<'JS'
import { compressAssets } from './scripts/compress-assets.mjs';
import { performance } from 'node:perf_hooks';
const samples = [];
for (let i = 0; i < 10; i++) {
  const start = performance.now();
  await compressAssets('dist/assets');
  samples.push(performance.now() - start);
}
console.log({ meanMs: samples.reduce((a, b) => a + b, 0) / samples.length });
JS
~~~

To verify the embedded HTTP responses from the repository root after building the frontend, inspect the generated staging cleanup before running it:

~~~sh
git clean -ndX -- cmd/fileament/dist
git clean -fdX -- cmd/fileament/dist
mkdir -p cmd/fileament/dist
cp -R web/dist/. cmd/fileament/dist/
go test -tags embedded_ui ./cmd/fileament -run TestEmbeddedUIServesWithoutExternalDirectory -count=1 -v
~~~

CI also runs the final image with disposable `/data` on tmpfs, a read-only root filesystem, dropped capabilities, a memory/CPU limit, and a loopback-only host port. The smoke test checks health, HTML, negotiated gzip, HEAD, conditional asset responses, and private/share cache policies, then removes its container. With a running Docker engine, run the same check from the repository root:

~~~sh
docker build -t fileament:ci .
python3 .github/scripts/smoke_container.py fileament:ci
~~~

Hashed scripts and styles have a one-year immutable cache lifetime and separate weak validators for identity and gzip. Forced validation can return a bodyless 304. Ordinary HTML remains refreshable with `no-cache`, and share HTML, API responses, and owner/public model assets retain `private, no-store`. Missing static assets return 404 instead of cached HTML. Tests cover GET/HEAD, quality-value negotiation, unsupported encodings, conditional requests, identity byte ranges, full gzip responses to range requests, and operation without a gzip copy.

Thumbnail notifications now enter a fixed 250 ms batch window. A controlled frontend regression sends 100 notifications for one model after the catalog's initial request. The previous implementation at `7c30256` made 100 additional catalog fetch calls; the batched implementation makes one. This is an in-process fetch-count measurement with mocked responses, not a production load or network-throughput benchmark. Repeat it with `npx vitest run src/AppThumbnailRefresh.test.tsx` from `web/`.

The queue retains at most 256 distinct model IDs; overflow requests full reconciliation. Refreshes are serialized, and events received during a request remain queued for another batch. If an affected query was already loading before the event, it is refreshed again after that older request settles. Active model details are targeted by model ID, and collection details by loaded membership; catalog and collection summaries refresh in batches. Server data determines the selected cover, and a regression test verifies that preview updates preserve an unsaved model title.

Catalog/model/collection subscriptions reconcile after reconnects and every minute, including when EventSource is unavailable. Existing pending-model polling remains at 15 seconds. Upload reconciliation checks pending models every 15 seconds and uses at most three concurrent model requests. Completed uploads are fetched again only for a relevant event or an overflow reconciliation. Explicitly queued events survive a simultaneous pending-upload reconciliation. Unmount closes streams, clears timers, aborts upload refreshes, and prevents queued callbacks from starting. Public pages do not subscribe to owner events.

## Metadata and tag updates

Model metadata updates compare saved fields and tag relationships within the existing immediate transaction. They update only changed columns, retain unchanged tag links, and add or remove only the changed links. Author, license, source URL, and timestamp changes no longer trigger full-text search maintenance. Changed titles, descriptions, and tag links still use the existing search triggers. The mutation recovery guard and durable sidecar publication remain in place.

Run the database benchmark from the repository root:

```sh
GOTOOLCHAIN=go1.26.8 go test ./internal/server -run '^$' \
  -bench '^BenchmarkModelMetadataUpdates$' -benchtime=10x -count=3
```

The benchmark uses one model with 10, 100, or 500 tags, one SQLite connection, WAL, full commit synchronization, and immediate transactions. Setup creates the model and both alternating tag labels before timing. Each operation advances `updatedAt`: `unchanged` measures a timestamp-only save, while the other cases alternate the author, title, or one tag. A separate regression test verifies zero database row changes for a true no-op with the same timestamp.

Local results on September 12, 2026, on Apple M5/macOS arm64 with Go 1.26.8 are below. Each value is the median of three runs, each reporting the mean of ten operations. The baseline ran the same benchmark harness against commit `3a843715ef63de4533068c31baa1970b11c0a97b`; reproducing it requires copying this benchmark test into an isolated checkout of that commit, which predates the harness.

| Tags | Changed fields | Time per operation, before → after | SQLite row changes per operation, before → after | Allocated bytes per operation, before → after |
| --- | --- | --- | --- | --- |
| 100 | Timestamp only | 8.60 ms → 0.143 ms | 2,791 → 1 | 58,830 → 20,516 |
| 100 | Author and timestamp | 8.40 ms → 0.123 ms | 2,791 → 1 | 58,707 → 20,662 |
| 100 | Title and timestamp | 8.47 ms → 0.215 ms | 2,791 → 11 | 58,648 → 20,712 |
| 100 | One tag and timestamp | 8.43 ms → 0.329 ms | 2,791 → 27.9 | 60,540 → 23,472 |
| 500 | Timestamp only | 147.09 ms → 0.381 ms | 14,920 → 1 | 289,923 → 112,784 |
| 500 | Author and timestamp | 146.13 ms → 0.377 ms | 14,920 → 1 | 290,021 → 112,929 |
| 500 | Title and timestamp | 146.88 ms → 0.721 ms | 15,077 → 16.8 | 290,050 → 112,979 |
| 500 | One tag and timestamp | 147.43 ms → 1.100 ms | 14,920 → 39.1 | 298,062 → 122,163 |

SQLite `total_changes()` includes internal full-text search row changes; it does not count disk writes, bytes, or synchronization calls. Fractional results reflect per-operation averages that include internal index maintenance. The benchmark measures direct database updates and excludes HTTP, authentication, model hydration, sidecar snapshots/publication, file payloads, and the frontend. These short runs with warm caches do not establish end-to-end save latency.

The implementation still scans the current and requested tag sets. Each changed relationship invokes the existing search trigger, so replacing every tag continues to cost more than retaining the same tags or changing one. No durability settings or search semantics were weakened to obtain these results.
