package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestMetadataUpdatesPreserveUnchangedTagLinks(t *testing.T) {
	app := newAuthedTestApp(t)
	app.db.SetMaxOpenConns(1)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Part")
	model.Tags = []string{"Tools", "3D Print"}
	if err := app.updateModel(model); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		if _, err := app.db.Exec(`CREATE TRIGGER reject_tag_` + op + ` BEFORE ` + op + ` ON model_tags BEGIN SELECT RAISE(ABORT,'unchanged tag relationship rewritten'); END`); err != nil {
			t.Fatal(err)
		}
	}
	model.Tags = []string{"tools", "3d-print"}
	model.Author = "New author"
	var before, after int64
	if err := app.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := app.updateModel(model); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after-before != 1 {
		t.Fatalf("author-only update made %d row changes; want one, without FTS writes", after-before)
	}
	got, err := app.getModel(model.ID)
	if err != nil || got.Author != "New author" || !reflect.DeepEqual(got.Tags, []string{"3D Print", "Tools"}) {
		t.Fatalf("model=%+v err=%v", got, err)
	}
	before = after
	if err := app.updateModel(got); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("unchanged metadata made %d row changes: %v", after-before, err)
	}
}

func TestTagsWithoutASCIISlugsRemainDistinctAndDurable(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"tags":["模型","支架","!!!","工具","Tools","tools","3D Print","3d-print"]}`))
	if rec.Code != 200 {
		t.Fatalf("patch=%d %s", rec.Code, rec.Body.String())
	}
	var m Model
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Tags) != 6 {
		t.Fatalf("tag labels collapsed: %v", m.Tags)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.AddCookie(fixture.cookie)
	tagsResponse := httptest.NewRecorder()
	app.Router().ServeHTTP(tagsResponse, req)
	var tags []struct{ Name, Slug string }
	if err := json.Unmarshal(tagsResponse.Body.Bytes(), &tags); err != nil || tagsResponse.Code != 200 {
		t.Fatalf("tags=%d err=%v", tagsResponse.Code, err)
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag.Slug == "" || seen[tag.Slug] {
			t.Fatalf("unfilterable tag: %+v", tag)
		}
		seen[tag.Slug] = true
		req := httptest.NewRequest(http.MethodGet, "/api/models?tag="+url.QueryEscape(tag.Slug), nil)
		req.AddCookie(fixture.cookie)
		result := httptest.NewRecorder()
		app.Router().ServeHTTP(result, req)
		var page struct{ Items []Model }
		if err := json.Unmarshal(result.Body.Bytes(), &page); err != nil || result.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != m.ID {
			t.Fatalf("tag filter=%d %s err=%v", result.Code, result.Body.String(), err)
		}
	}
	inspectBackup(t, app, fixture.cookie, downloadBackup(t, app, fixture.cookie))
	assertMutationRestarts(t, app, snapshotLibrary(t, app))
}

func TestMigrationPreservesLegacyEmptyTagIDs(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.db.Exec(`INSERT INTO models(id,title,created_at,updated_at) VALUES('legacy','Legacy',1,1);
 INSERT INTO tags(id,name,slug) VALUES('legacy-tag','模型','');
 INSERT INTO model_tags(model_id,tag_id) VALUES('legacy','legacy-tag');
 PRAGMA user_version=5;`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(app.db); err != nil {
		t.Fatal(err)
	}
	var id, name, slug string
	if err := app.db.QueryRow(`SELECT tags.id,tags.name,tags.slug FROM tags JOIN model_tags ON tags.id=model_tags.tag_id WHERE model_tags.model_id='legacy'`).Scan(&id, &name, &slug); err != nil {
		t.Fatal(err)
	}
	if id != "legacy-tag" || name != "模型" || slug == "" {
		t.Fatalf("legacy tag not preserved/filterable: %q %q %q", id, name, slug)
	}
	m, err := app.getModel("legacy")
	if err != nil {
		t.Fatal(err)
	}
	m.Author = "Edited after migration"
	if err := app.updateModel(m); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(`SELECT tag_id FROM model_tags WHERE model_id='legacy'`).Scan(&id); err != nil || id != "legacy-tag" {
		t.Fatalf("legacy tag ID changed: %q %v", id, err)
	}
}

func TestRestoreMigratesLegacyEmptyTag(t *testing.T) {
	source := newAuthedTestApp(t)
	cookie := loginCookie(t, source, "password-password")
	m := uploadSTLModel(t, source, cookie, "legacy.stl", "Legacy")
	if _, err := source.db.Exec(`INSERT INTO tags(id,name,slug) VALUES('legacy-tag','模型','');
 INSERT INTO model_tags(model_id,tag_id) VALUES(?,'legacy-tag');
 PRAGMA user_version=5;`, m.ID); err != nil {
		t.Fatal(err)
	}
	m, err := source.getModel(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.writeSidecar(m); err != nil {
		t.Fatal(err)
	}
	backup := downloadBackup(t, source, cookie)
	destination := newAuthedTestApp(t)
	destinationCookie := loginCookie(t, destination, "password-password")
	inspection := inspectBackup(t, destination, destinationCookie, backup)
	if inspection.Manifest.DatabaseVersion != 5 {
		t.Fatalf("backup version=%d", inspection.Manifest.DatabaseVersion)
	}
	rec := serveMutationRequest(destination, destinationCookie, jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore=%d %s", rec.Code, rec.Body.String())
	}
	destinationCookie = loginCookie(t, destination, "password-password")
	rec = serveMutationRequest(destination, destinationCookie, jsonReq(http.MethodPatch, "/api/models/"+m.ID, `{"author":"After restore"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-restore edit=%d %s", rec.Code, rec.Body.String())
	}
	var id, name, slug string
	if err := destination.db.QueryRow(`SELECT tags.id,tags.name,tags.slug FROM tags JOIN model_tags ON tags.id=model_tags.tag_id WHERE model_tags.model_id=?`, m.ID).Scan(&id, &name, &slug); err != nil {
		t.Fatal(err)
	}
	if id != "legacy-tag" || name != "模型" || slug == "" {
		t.Fatalf("restored tag=%q %q %q", id, name, slug)
	}
	var version int
	if err := destination.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("restored version=%d err=%v", version, err)
	}
	assertMutationRestarts(t, destination, snapshotLibrary(t, destination))
}

func TestNonASCIITagSurvivesFreshIndex(t *testing.T) {
	fixture := newMutationFixture(t)
	rec := serveMutationRequest(fixture.app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"tags":["模型"]}`))
	if rec.Code != 200 {
		t.Fatalf("patch=%d %s", rec.Code, rec.Body.String())
	}
	assertMutationRestarts(t, fixture.app, snapshotLibrary(t, fixture.app))
}

func TestChangedMetadataAndTagDeltaUpdateSearch(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	for _, body := range []string{`{"title":"OldTitle","tags":["keep","oldtag"]}`, `{"title":"NewTitle","description":"FreshDescription","author":"Author","tags":["keep","newtag"]}`} {
		rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, body))
		if rec.Code != 200 {
			t.Fatalf("patch=%d %s", rec.Code, rec.Body.String())
		}
		if body == `{"title":"OldTitle","tags":["keep","oldtag"]}` {
			shared := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.other.ID, `{"tags":["oldtag"]}`))
			if shared.Code != 200 {
				t.Fatal(shared.Code, shared.Body.String())
			}
			if _, err := app.db.Exec(`CREATE TRIGGER retain_keep BEFORE DELETE ON model_tags WHEN old.tag_id='tag_keep' BEGIN SELECT RAISE(ABORT,'retained tag removed'); END;`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := app.db.Exec(`DROP TRIGGER retain_keep`); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{"OldTitle": 0, "oldtag": 0, "NewTitle": 1, "FreshDescription": 1, "keep": 1, "newtag": 1} {
		var got int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM models_fts WHERE models_fts MATCH ? AND rowid=(SELECT rowid FROM models WHERE id=?)`, query, fixture.model.ID).Scan(&got); err != nil || got != want {
			t.Fatalf("search %s=%d want=%d err=%v", query, got, want, err)
		}
	}
	inspectBackup(t, app, fixture.cookie, downloadBackup(t, app, fixture.cookie))
	assertMutationRestarts(t, app, snapshotLibrary(t, app))
}

func TestTagUpdateFailureRollsBackMetadataAndSearch(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"title":"OriginalTitle","tags":["keep","oldtag"]}`))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	before := snapshotLibrary(t, app)
	if _, err := app.db.Exec(`CREATE TRIGGER fail_new_tag BEFORE INSERT ON model_tags WHEN new.tag_id='tag_newtag' BEGIN SELECT RAISE(ABORT,'injected tag failure'); END`); err != nil {
		t.Fatal(err)
	}
	rec = serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"title":"FailedTitle","tags":["keep","newtag"]}`))
	if rec.Code != 500 {
		t.Fatalf("failure=%d %s", rec.Code, rec.Body.String())
	}
	if _, err := app.db.Exec(`DROP TRIGGER fail_new_tag`); err != nil {
		t.Fatal(err)
	}
	if got := snapshotLibrary(t, app); !reflect.DeepEqual(got, before) {
		t.Fatal("failed update changed durable state")
	}
	for query, want := range map[string]int{"OriginalTitle": 1, "oldtag": 1, "FailedTitle": 0, "newtag": 0} {
		var got int
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM models_fts WHERE models_fts MATCH ? AND rowid=(SELECT rowid FROM models WHERE id=?)`, query, fixture.model.ID).Scan(&got); err != nil || got != want {
			t.Fatalf("rollback search %s=%d want=%d err=%v", query, got, want, err)
		}
	}
	assertMutationRestarts(t, app, before)
}

func TestEmptyTagMigrationConflictRollsBack(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.db.Exec(`INSERT INTO tags(id,name,slug) VALUES('legacy','模型',''),('conflict','Conflict',?); PRAGMA user_version=5`, tagSlug("模型")); err != nil {
		t.Fatal(err)
	}
	if err := migrate(app.db); err == nil {
		t.Fatal("expected conflicting tag migration to fail")
	}
	var slug string
	var version int
	if err := app.db.QueryRow(`SELECT slug FROM tags WHERE id='legacy'`).Scan(&slug); err != nil || slug != "" {
		t.Fatalf("migration changed legacy slug=%q err=%v", slug, err)
	}
	if err := app.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("migration advanced version=%d err=%v", version, err)
	}
}
