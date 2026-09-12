package server

import (
	"strings"
	"testing"
)

func TestStartupJobQueriesUseFileIndex(t *testing.T) {
	app := newTestApp(t)
	for _, query := range []string{
		`SELECT COUNT(*) FROM jobs WHERE file_id='file' AND type='thumbnail'`,
		`SELECT f.id FROM files f WHERE f.model_id='model' AND COALESCE(f.thumb_path,'')='' AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.file_id=f.id AND j.type='thumbnail')`,
		`SELECT id FROM jobs WHERE file_id='file' AND type='thumbnail' ORDER BY created_at DESC,id DESC LIMIT 1`,
	} {
		plan := queryPlan(t, app.db, query)
		if !strings.Contains(plan, "jobs_file_order") || strings.Contains(plan, "jobs_pending_") || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("per-file lookup scans unrelated work: %s", plan)
		}
	}
}
