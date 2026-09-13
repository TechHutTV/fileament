package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func BenchmarkCatalogCards(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			app := &App{db: benchmarkCatalogDB(b, size, true)}
			if _, err := app.db.Exec(`UPDATE files SET sha256=?`, strings.Repeat("0", 64)); err != nil {
				b.Fatal(err)
			}
			for _, summary := range []bool{false, true} {
				b.Run(fmt.Sprint("summary=", summary), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var items any
						next := ""
						if summary {
							cards, err := app.queryModelSummaries(context.Background(), "", "1=1", "models.created_at DESC,models.id DESC", "0", 25)
							if err != nil {
								b.Fatal(err)
							}
							last := cards[23]
							next = encodeCursor("created", Model{ID: last.ID, CreatedAt: last.CreatedAt})
							items = cards[:24]
						} else {
							rows, err := app.db.Query(`SELECT DISTINCT id FROM models ORDER BY created_at DESC,id DESC LIMIT 25`)
							if err != nil {
								b.Fatal(err)
							}
							var ids []string
							for rows.Next() {
								var id string
								if err := rows.Scan(&id); err != nil {
									b.Fatal(err)
								}
								ids = append(ids, id)
							}
							if err := rows.Err(); err != nil {
								b.Fatal(err)
							}
							if err := rows.Close(); err != nil {
								b.Fatal(err)
							}
							last, err := app.getModel(ids[23])
							if err != nil {
								b.Fatal(err)
							}
							next = encodeCursor("created", last)
							models := []Model{}
							for _, id := range ids[:24] {
								m, err := app.getModel(id)
								if err != nil {
									b.Fatal(err)
								}
								models = append(models, m)
							}
							items = models
						}
						encoded, err := json.Marshal(map[string]any{"items": items, "nextCursor": next})
						if err != nil {
							b.Fatal(err)
						}
						b.ReportMetric(float64(len(encoded)), "payload-bytes")
					}
				})
			}
		})
	}
}
