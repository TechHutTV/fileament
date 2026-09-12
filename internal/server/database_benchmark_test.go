package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkDatabaseConcurrency(b *testing.B) {
	for _, setup := range []struct {
		mode      string
		pool      int
		immediate bool
	}{
		{"DELETE", 0, false},
		{"DELETE", 4, true},
		{"WAL", 1, true},
		{"WAL", 2, true},
		{"WAL", 4, true},
		{"WAL", 8, true},
	} {
		b.Run(fmt.Sprintf("%s/pool=%d/immediate=%t", setup.mode, setup.pool, setup.immediate), func(b *testing.B) {
			fixture := benchmarkCatalogDB(b, 1000, true)
			var seq int
			var name, path string
			if err := fixture.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
				b.Fatal(err)
			}
			if err := fixture.Close(); err != nil {
				b.Fatal(err)
			}
			u := url.URL{Scheme: "file", Path: path}
			q := url.Values{"_pragma": {"foreign_keys(ON)", "busy_timeout(5000)", "synchronous(FULL)"}}
			if setup.immediate {
				q.Set("_txlock", "immediate")
			}
			u.RawQuery = q.Encode()
			db, err := sql.Open("sqlite", u.String())
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(setup.pool)
			if setup.pool > 0 {
				db.SetMaxIdleConns(setup.pool)
			}
			if _, err := db.Exec(`PRAGMA journal_mode=` + setup.mode); err != nil {
				b.Fatal(err)
			}
			app := &App{db: db}
			var next, failures, readTime, reads atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := next.Add(1)
					var err error
					switch n % 8 {
					case 0:
						app.modelPersistMu.Lock()
						app.mutationMu.Lock()
						err = benchmarkImport(db, n)
						app.mutationMu.Unlock()
						app.modelPersistMu.Unlock()
					case 1:
						var job string
						job, _, err = app.claimThumbnailJob(context.Background())
						if err == nil && job != "" {
							err = app.finishThumbnailJob(job, "done", "")
						}
					default:
						start := time.Now()
						_, err = app.queryModelSummaries(context.Background(), "", "1=1", "models.created_at DESC,models.id DESC", "0", 25)
						readTime.Add(time.Since(start).Nanoseconds())
						reads.Add(1)
					}
					if err != nil {
						failures.Add(1)
					}
				}
			})
			b.StopTimer()
			b.ReportMetric(float64(failures.Load()), "errors")
			b.ReportMetric(float64(readTime.Load())/float64(reads.Load()), "read-ns/op")
			b.ReportMetric(float64(db.Stats().WaitCount), "pool-waits")
			b.ReportMetric(float64(db.Stats().OpenConnections), "retained-conns")
		})
	}
}

func benchmarkImport(db *sql.DB, n int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := fmt.Sprintf("import-%09d", n)
	if _, err := tx.Exec(`INSERT INTO models(id,title,description,total_bytes,created_at,updated_at) VALUES(?,?,'',100,?,?)`, id, id, n, n); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sha256,sort_order) VALUES(?,?,'part.stl','files/part.stl','stl',100,'',0)`, id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,'thumbnail',?,'pending',?)`, id, id, n); err != nil {
		return err
	}
	return tx.Commit()
}
