package server

import (
	"fmt"
	"testing"
)

func BenchmarkModelMetadataUpdates(b *testing.B) {
	for _, count := range []int{10, 100, 500} {
		for _, kind := range []string{"unchanged", "author", "title", "one-tag"} {
			b.Run(fmt.Sprintf("tags=%d/%s", count, kind), func(b *testing.B) {
				db, err := openDatabase(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				defer db.Close()
				db.SetMaxOpenConns(1)
				if err := migrate(db); err != nil {
					b.Fatal(err)
				}
				app := &App{db: db}
				m := Model{ID: "bench-model", Title: "Metadata", Description: "Searchable description", CreatedAt: 1, UpdatedAt: 1}
				if _, err := db.Exec(`INSERT INTO models(id,title,description,created_at,updated_at) VALUES(?,?,?,?,?)`, m.ID, m.Title, m.Description, 1, 1); err != nil {
					b.Fatal(err)
				}
				for i := 0; i < count; i++ {
					m.Tags = append(m.Tags, fmt.Sprintf("tag-%03d", i))
				}
				if err := app.updateModel(m); err != nil {
					b.Fatal(err)
				}
				// Create both alternating tag labels before measuring.
				if _, err := db.Exec(`INSERT INTO tags(id,name,slug) VALUES('tag_alternate','alternate','alternate')`); err != nil {
					b.Fatal(err)
				}
				var before, after int64
				if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					next := m
					next.UpdatedAt = int64(i + 2)
					switch kind {
					case "author":
						next.Author = fmt.Sprint(i % 2)
					case "title":
						next.Title = fmt.Sprintf("Title %d", i%2)
					case "one-tag":
						next.Tags = append([]string(nil), m.Tags...)
						if i%2 == 0 {
							next.Tags[0] = "alternate"
						}
					}
					if err := app.updateModel(next); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(after-before)/float64(b.N), "row-changes/op")
			})
		}
	}
}
