package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/ids"
)

func TestCatalogReturnsOnlyCardData(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Card model")
	if _, err := app.db.Exec(`UPDATE models SET description=? WHERE id=?`, strings.Repeat("Detailed instructions. ", 1000), model.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("status=%d page=%+v err=%v", rec.Code, page, err)
	}
	for _, field := range []string{"description", "sourceUrl", "images", "tags"} {
		if _, ok := page.Items[0][field]; ok {
			t.Errorf("card contains detail field %q", field)
		}
	}
	var files []map[string]json.RawMessage
	if err := json.Unmarshal(page.Items[0]["files"], &files); err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if _, ok := files[0]["relPath"]; ok {
		t.Error("card contains file storage metadata")
	}
}

func collectionPageFixture(t *testing.T, count int) (*App, *http.Cookie, Collection, []Model) {
	t.Helper()
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	models := make([]Model, count)
	collection := Collection{ID: ids.New(), Name: "Many models", Slug: "many-models", CreatedAt: 1}
	if _, err := app.db.Exec(`INSERT INTO collections(id,name,slug,created_at) VALUES(?,?,?,?)`, collection.ID, collection.Name, collection.Slug, collection.CreatedAt); err != nil {
		t.Fatal(err)
	}
	for i := range models {
		m := Model{ID: ids.New(), Title: fmt.Sprintf("Model %03d", i), Description: "Detail for " + fmt.Sprint(i), Files: []ModelFile{}, CreatedAt: int64(i / 3), UpdatedAt: int64(i / 2)}
		if err := os.MkdirAll(filepath.Join(app.cfg.DataDir, "models", m.ID), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := app.insertModel(m); err != nil {
			t.Fatal(err)
		}
		if err := app.writeSidecar(m); err != nil {
			t.Fatal(err)
		}
		if _, err := app.db.Exec(`INSERT INTO collection_models(collection_id,model_id,sort_order) VALUES(?,?,?)`, collection.ID, m.ID, i/2); err != nil {
			t.Fatal(err)
		}
		models[i] = m
		collection.ModelIDs = append(collection.ModelIDs, m.ID)
	}
	if err := app.writeCollectionsSidecar(); err != nil {
		t.Fatal(err)
	}
	return app, cookie, collection, models
}

func TestCollectionPagesAreBoundedOrderedAndScoped(t *testing.T) {
	app, cookie, collection, models := collectionPageFixture(t, 107)
	app.db.SetMaxOpenConns(1)
	cursor := ""
	var seen []string
	for {
		path := "/api/collections/" + collection.Slug + "?limit=17&cursor=" + url.QueryEscape(cursor)
		rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodGet, path, nil))
		var page collectionPage
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 {
			t.Fatalf("status=%d err=%v body=%s", rec.Code, err, rec.Body.String())
		}
		if len(page.Models) > 17 || len(page.ModelIDs) != len(page.Models) || page.ModelCount != 107 {
			t.Fatalf("invalid page: %+v", page)
		}
		for i, m := range page.Models {
			if m.ID != page.ModelIDs[i] {
				t.Fatal("membership and card order differ")
			}
			seen = append(seen, m.ID)
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(seen, collection.ModelIDs) {
		t.Fatal("pages lost or duplicated members with tied positions")
	}
	page, err := app.getCollectionPage(context.Background(), collection.ID, 100, "")
	if err != nil || len(page.Models) != 100 || page.NextCursor == "" {
		t.Fatalf("maximum page=%d err=%v", len(page.Models), err)
	}
	for _, bad := range []string{"invalid", encodeCursor("collection:foreign", Model{ID: models[0].ID, Title: "0"}), encodeCursor("collection:"+collection.ID, Model{ID: models[0].ID, Title: "overflow99999999999999999999"})} {
		rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodGet, "/api/collections/"+collection.ID+"?cursor="+url.QueryEscape(bad), nil))
		if rec.Code != 400 {
			t.Fatalf("invalid cursor status=%d", rec.Code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.getCollectionPage(ctx, collection.ID, 24, ""); err == nil {
		t.Fatal("canceled page query succeeded")
	}
}

func TestPublicCollectionReturnsOneSelectedModelAndChecksCurrentScope(t *testing.T) {
	app, cookie, collection, models := collectionPageFixture(t, 30)
	shareResponse := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"collection","targetId":"`+collection.ID+`"}`))
	var share ShareLink
	if err := json.Unmarshal(shareResponse.Body.Bytes(), &share); err != nil || shareResponse.Code != 201 {
		t.Fatalf("share status=%d err=%v", shareResponse.Code, err)
	}
	request := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+query, nil))
		return rec
	}
	rec := request("?model=" + models[29].ID + "&limit=2")
	var result struct {
		Model      Model          `json:"model"`
		Collection collectionPage `json:"collection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 {
		t.Fatalf("public status=%d err=%v", rec.Code, err)
	}
	if result.Model.ID != models[29].ID || result.Model.Description != models[29].Description || len(result.Collection.Models) != 2 || result.Collection.NextCursor == "" {
		t.Fatalf("wrong selected model/page: %+v", result)
	}
	if strings.Contains(rec.Body.String(), "Detail for 0") {
		t.Fatal("unselected model details leaked into cards")
	}
	if rec.Header().Get("X-Robots-Tag") != "noindex" || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("public response lost security headers")
	}
	remove := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodDelete, "/api/collections/"+collection.ID+"/models/"+models[29].ID, nil))
	if remove.Code != 204 {
		t.Fatalf("remove=%d body=%s", remove.Code, remove.Body.String())
	}
	if got := request("?model=" + models[29].ID); got.Code != 404 {
		t.Fatalf("removed member=%d", got.Code)
	}
	if got := request("?model=unrelated"); got.Code != 404 {
		t.Fatalf("foreign member=%d", got.Code)
	}
	if _, err := app.db.Exec(`UPDATE share_links SET revoked_at=1 WHERE id=?`, share.ID); err != nil {
		t.Fatal(err)
	}
	if got := request("?cursor=" + url.QueryEscape(result.Collection.NextCursor)); got.Code != 410 {
		t.Fatalf("revoked page=%d", got.Code)
	}
}

func TestCompactCollectionMoveCrossesPageBoundaryAndSurvivesRebuild(t *testing.T) {
	app, cookie, collection, models := collectionPageFixture(t, 26)
	rec := serveMutationRequest(app, cookie, jsonReq(http.MethodPut, "/api/collections/"+collection.ID+"/order", `{"modelId":"`+models[23].ID+`","direction":"down"}`))
	if rec.Code != 204 {
		t.Fatalf("move=%d body=%s", rec.Code, rec.Body.String())
	}
	page, err := app.getCollectionPage(context.Background(), collection.ID, 24, "")
	if err != nil || page.Models[23].ID != models[24].ID {
		t.Fatalf("first page boundary err=%v", err)
	}
	next, err := app.getCollectionPage(context.Background(), collection.ID, 24, page.NextCursor)
	if err != nil || next.Models[0].ID != models[23].ID {
		t.Fatalf("second page boundary err=%v", err)
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	assertMutationRestarts(t, app, snapshotLibrary(t, app))
}

func TestCatalogFiltersHaveNoDuplicateJoinResults(t *testing.T) {
	app, cookie, collection, models := collectionPageFixture(t, 4)
	if _, err := app.db.Exec(`INSERT INTO collections(id,name,slug,created_at) VALUES('other','Other',?,1)`, collection.ID); err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if _, err := app.db.Exec(`INSERT INTO collection_models(collection_id,model_id,sort_order) VALUES('other',?,0)`, m.ID); err != nil {
			t.Fatal(err)
		}
		m.Tags = []string{"Tools"}
		if err := app.updateModel(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, sort := range []string{"created", "updated", "title", "size"} {
		cursor := ""
		seen := map[string]bool{}
		for {
			rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodGet, "/api/models?limit=1&q=Model&tag=tools&collection="+collection.ID+"&sort="+sort+"&cursor="+url.QueryEscape(cursor), nil))
			var page struct {
				Items      []modelSummary `json:"items"`
				NextCursor string         `json:"nextCursor"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 {
				t.Fatalf("status=%d err=%v", rec.Code, err)
			}
			for _, m := range page.Items {
				if seen[m.ID] {
					t.Fatal("duplicate filtered model")
				}
				seen[m.ID] = true
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 4 {
			t.Fatalf("sort=%s returned %d models", sort, len(seen))
		}
	}
}

func TestPublicCollectionCannotResolveAnotherCollectionsSlug(t *testing.T) {
	app, cookie, collection, _ := collectionPageFixture(t, 1)
	shareResponse := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"collection","targetId":"`+collection.ID+`"}`))
	var share ShareLink
	if err := json.Unmarshal(shareResponse.Body.Bytes(), &share); err != nil || shareResponse.Code != 201 {
		t.Fatalf("share status=%d err=%v", shareResponse.Code, err)
	}
	rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodDelete, "/api/collections/"+collection.ID, nil))
	if rec.Code != 204 {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if _, err := app.db.Exec(`INSERT INTO collections(id,name,slug,created_at) VALUES(?,'Private collection',?,1)`, ids.New(), collection.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token, nil))
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "Private collection") {
		t.Fatalf("foreign collection metadata=%d %s", rec.Code, rec.Body.String())
	}
}

func TestCardQueriesUseIndexesForOrderingAndMembership(t *testing.T) {
	app := newAuthedTestApp(t)
	for _, test := range []struct{ name, where, order, index string }{
		{"created", "1=1", "models.created_at DESC,models.id DESC", "models_created_id"},
		{"updated", "1=1", "models.updated_at DESC,models.id DESC", "models_updated_id"},
		{"title", "1=1", "models.title COLLATE NOCASE,models.id", "models_title_id"},
		{"size", "1=1", "models.total_bytes DESC,models.id DESC", "models_size_id"},
		{"tag", `models.id IN (SELECT mt.model_id FROM model_tags mt JOIN tags tg ON tg.id=mt.tag_id WHERE tg.slug='tools')`, "models.created_at DESC,models.id DESC", "model_tags_tag"},
		{"collection", `models.id IN (SELECT cm.model_id FROM collection_models cm JOIN collections c ON c.id=cm.collection_id WHERE c.id='collection' OR c.slug='collection')`, "models.created_at DESC,models.id DESC", "collection_models_order"},
	} {
		plan := queryPlan(t, app.db, `SELECT `+cardColumns+` FROM models`+cardJoin+` WHERE `+test.where+` ORDER BY `+test.order+` LIMIT 25`)
		if !strings.Contains(plan, test.index) {
			t.Errorf("%s missing %s: %s", test.name, test.index, plan)
		}
	}
}
