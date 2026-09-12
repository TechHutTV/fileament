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
