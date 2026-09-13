package server

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var indexedQueries = []struct {
	name, query, index string
}{
	{"created", `SELECT DISTINCT id FROM models ORDER BY created_at DESC, id DESC LIMIT 25`, "models_created_id"},
	{"updated", `SELECT DISTINCT id FROM models ORDER BY updated_at DESC, id DESC LIMIT 25`, "models_updated_id"},
	{"title", `SELECT DISTINCT id FROM models ORDER BY title COLLATE NOCASE, id LIMIT 25`, "models_title_id"},
	{"size", `SELECT DISTINCT id FROM models ORDER BY total_bytes DESC, id DESC LIMIT 25`, "models_size_id"},
	{"files", `SELECT id FROM files WHERE model_id = '00000500' ORDER BY sort_order, filename`, "files_model_order"},
	{"images", `SELECT id FROM images WHERE model_id = '00000500' ORDER BY sort_order`, "images_model_order"},
	{"pending", `SELECT id,file_id FROM jobs WHERE type='thumbnail' AND status='pending' ORDER BY created_at, id LIMIT 1`, "jobs_pending_order"},
	{"file-jobs", `SELECT id FROM jobs WHERE file_id='00000500-0'`, "jobs_file_order"},
	{"collection-order", `SELECT model_id FROM collection_models WHERE collection_id='collection' ORDER BY sort_order`, "collection_models_order"},
	{"model-membership", `SELECT collection_id FROM collection_models WHERE model_id='00000500'`, "collection_models_model"},
	{"tag-models", `SELECT model_id FROM model_tags WHERE tag_id='tag'`, "model_tags_tag"},
	{"sessions", `SELECT token FROM sessions WHERE expires_at < 100`, "sessions_expiry"},
}

func TestQueryIndexMigrationPlans(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("from-%d", version), func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "index.db"))+"?_pragma=foreign_keys(ON)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if version > 0 {
				if _, err := db.Exec(schema + `PRAGMA user_version = 1`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO models(id,title,created_at,updated_at) VALUES('kept','Kept model',1,1)`); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 2 {
				if _, err := db.Exec(jobLifecycleMigration); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 3 {
				if _, err := db.Exec(queryIndexesMigration); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 4 {
				if _, err := db.Exec(thumbnailRetryMigration); err != nil {
					t.Fatal(err)
				}
			}
			if version >= 5 {
				if _, err := db.Exec(thumbnailFileIndexMigration); err != nil {
					t.Fatal(err)
				}
			}
			if version == 6 {
				if _, err := db.Exec(`PRAGMA user_version = 6`); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrate(db); err != nil {
				t.Fatal(err)
			}
			if version > 0 {
				var title string
				if err := db.QueryRow(`SELECT title FROM models WHERE id='kept'`).Scan(&title); err != nil || title != "Kept model" {
					t.Fatalf("migration lost data: title=%s err=%v", title, err)
				}
			}
			for _, query := range indexedQueries {
				plan := queryPlan(t, db, query.query)
				if !strings.Contains(plan, query.index) || strings.Contains(plan, "TEMP B-TREE FOR ORDER BY") {
					t.Errorf("%s: %s", query.name, plan)
				}
			}
		})
	}
}

func queryPlan(t testing.TB, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "; ")
}

func BenchmarkCatalogIndexes(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		for _, indexed := range []bool{false, true} {
			b.Run(fmt.Sprintf("models-%d/indexed-%v", size, indexed), func(b *testing.B) {
				db := benchmarkCatalogDB(b, size, indexed)
				for _, q := range indexedQueries[:7] {
					b.Run(q.name, func(b *testing.B) {
						b.ReportAllocs()
						for i := 0; i < b.N; i++ {
							rows, err := db.Query(q.query)
							if err != nil {
								b.Fatal(err)
							}
							for rows.Next() {
							}
							if err := rows.Err(); err != nil {
								b.Fatal(err)
							}
							if err := rows.Close(); err != nil {
								b.Fatal(err)
							}
						}
					})
				}
			})
		}
	}
}

func benchmarkCatalogDB(b *testing.B, size int, indexed bool) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(b.TempDir(), "catalog.db"))+"?_pragma=foreign_keys(ON)")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(schema + jobLifecycleMigration); err != nil {
		b.Fatal(err)
	}
	if indexed {
		if err := migrate(db); err != nil {
			b.Fatal(err)
		}
	}
	start := time.Now()
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < size; i++ {
		id := fmt.Sprintf("%08d", i)
		if _, err := tx.Exec(`INSERT INTO models(id,title,description,total_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, "Model "+id, strings.Repeat("Catalog description. ", 16), i*100, i/10, i/5); err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			fileID := fmt.Sprintf("%s-%d", id, j)
			if _, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sort_order) VALUES(?,?,'part.stl','files/part.stl','stl',100,?)`, fileID, id, j); err != nil {
				b.Fatal(err)
			}
			status := "done"
			if i >= size-10 {
				status = "pending"
			}
			if i >= size-333 {
				if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,'thumbnail',?,?,?)`, fileID, fileID, status, i); err != nil {
					b.Fatal(err)
				}
			}
		}
		if _, err := tx.Exec(`INSERT INTO images(id,model_id,rel_path,sort_order) VALUES(?,?,'images/part.png',0)`, id, id); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	elapsed := time.Since(start)
	var pages, pageSize int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		b.Fatal(err)
	}
	b.Logf("fixture: %d models, 3 files and 1 image per model, 999 jobs (30 pending); insert=%s database=%d bytes", size, elapsed, pages*pageSize)
	return db
}
