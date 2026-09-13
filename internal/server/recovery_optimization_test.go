package server

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUnchangedRecoveryDoesNotRewriteIndex(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"tags":["fixtures","tools"]}`))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, table := range []string{"models", "files", "images", "tags", "model_tags", "collections", "collection_models", "jobs"} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			query := fmt.Sprintf(`CREATE TRIGGER reject_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'unchanged recovery wrote %s'); END`, table, operation, operation, table, table)
			if _, err := app.db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := app.rebuildFromSidecars(); err != nil {
		t.Fatal(err)
	}
	if err := app.rebuildCollectionsFromSidecar(); err != nil {
		t.Fatal(err)
	}
}

func TestUnchangedRecoveryRepairsMissingJobsAndSearchRows(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Searchable")
	if _, err := app.db.Exec(`DELETE FROM jobs`); err != nil {
		t.Fatal(err)
	}
	if err := app.rebuildFromSidecars(); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE file_id=? AND status='pending'`, model.Files[0].ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("missing preview work was not restored: %d %v", jobs, err)
	}
	if _, err := app.db.Exec(`DELETE FROM models_fts WHERE rowid=(SELECT rowid FROM models WHERE id=?)`, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.rebuildFromSidecars(); err != nil {
		t.Fatal(err)
	}
	var searchable int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM models_fts WHERE models_fts MATCH 'Searchable'`).Scan(&searchable); err != nil || searchable != 1 {
		t.Fatalf("missing search row was not restored: %d %v", searchable, err)
	}
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE file_id=?`, model.Files[0].ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("recovery duplicated jobs: %d %v", jobs, err)
	}
}
