package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPublicRefreshKeepsCurrentScopeWithoutCountingViews(t *testing.T) {
	app, cookie, collection, models := collectionPageFixture(t, 3)
	create := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"collection","targetId":"`+collection.ID+`"}`))
	var share ShareLink
	if err := json.Unmarshal(create.Body.Bytes(), &share); err != nil || create.Code != 201 {
		t.Fatalf("create=%d err=%v", create.Code, err)
	}
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+query, nil))
		return rec
	}
	initial := get("?limit=1&model=" + models[2].ID)
	var first struct {
		Collection collectionPage `json:"collection"`
	}
	if err := json.Unmarshal(initial.Body.Bytes(), &first); err != nil || initial.Code != 200 {
		t.Fatalf("initial=%d err=%v", initial.Code, err)
	}
	assertShareHits(t, app, share.ID, 1)
	for _, query := range []string{"?refresh=1&model=" + models[2].ID, "?refresh=1&limit=1&cursor=" + url.QueryEscape(first.Collection.NextCursor)} {
		rec := get(query)
		if rec.Code != 200 {
			t.Fatalf("refresh=%d body=%s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("X-Robots-Tag") != "noindex" || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("missing public security headers")
		}
	}
	assertShareHits(t, app, share.ID, 1)
	remove := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodDelete, "/api/collections/"+collection.ID+"/models/"+models[2].ID, nil))
	if remove.Code != 204 {
		t.Fatalf("remove=%d", remove.Code)
	}
	for _, id := range []string{models[2].ID, "unrelated"} {
		rec := get("?refresh=1&model=" + id)
		var result struct {
			Model            *Model         `json:"model"`
			ModelUnavailable bool           `json:"modelUnavailable"`
			Collection       collectionPage `json:"collection"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 {
			t.Fatalf("removed refresh=%d err=%v body=%s", rec.Code, err, rec.Body.String())
		}
		if result.Model != nil || !result.ModelUnavailable || result.Collection.ModelCount != 2 || strings.Contains(rec.Body.String(), models[2].Description) {
			t.Fatalf("invalid scope: %s", rec.Body.String())
		}
		if rec := get("?model=" + id); rec.Code != 404 {
			t.Fatalf("explicit invalid selection=%d", rec.Code)
		}
	}
	if rec := get("?refresh=1&cursor=invalid"); rec.Code != 400 {
		t.Fatalf("invalid cursor=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 1)
	if _, err := app.db.Exec(`UPDATE share_links SET revoked_at=1 WHERE id=?`, share.ID); err != nil {
		t.Fatal(err)
	}
	if rec := get("?refresh=1"); rec.Code != 410 {
		t.Fatalf("revoked refresh=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 1)
}

func TestPublicModelRefreshReflectsEditsAndChecksTarget(t *testing.T) {
	app, cookie, _, models := collectionPageFixture(t, 1)
	create := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+models[0].ID+`"}`))
	var share ShareLink
	if err := json.Unmarshal(create.Body.Bytes(), &share); err != nil || create.Code != 201 {
		t.Fatalf("create=%d err=%v", create.Code, err)
	}
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"?refresh=1", nil))
		return rec
	}
	if _, err := app.db.Exec(`UPDATE models SET title='Edited title' WHERE id=?`, models[0].ID); err != nil {
		t.Fatal(err)
	}
	rec := get()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Edited title") {
		t.Fatalf("edited=%d body=%s", rec.Code, rec.Body.String())
	}
	assertShareHits(t, app, share.ID, 0)
	if _, err := app.db.Exec(`UPDATE share_links SET expires_at=1 WHERE id=?`, share.ID); err != nil {
		t.Fatal(err)
	}
	if rec := get(); rec.Code != 410 {
		t.Fatalf("expired=%d", rec.Code)
	}
	if _, err := app.db.Exec(`UPDATE share_links SET expires_at=NULL,target_id='missing' WHERE id=?`, share.ID); err != nil {
		t.Fatal(err)
	}
	if rec := get(); rec.Code != 404 {
		t.Fatalf("missing target=%d", rec.Code)
	}
}
