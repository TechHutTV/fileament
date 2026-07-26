package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogListSearchDetailPatchAndAssets(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	first := uploadSTLModel(t, app, cookie, "gear.stl", "Gear model")
	second := uploadSTLModel(t, app, cookie, "bracket.stl", "Bracket model")

	req := httptest.NewRequest(http.MethodGet, "/api/models?limit=1", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items      []Model `json:"items"`
		NextCursor string  `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("unexpected first page %#v", page)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/models?q=gear", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d", rec.Code)
	}
	page = struct {
		Items      []Model `json:"items"`
		NextCursor string  `json:"nextCursor"`
	}{}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || !strings.Contains(strings.ToLower(page.Items[0].Title), "gear") {
		t.Fatalf("unexpected search page %#v", page)
	}

	req = httptest.NewRequest(http.MethodPatch, "/api/models/"+first.ID, strings.NewReader(`{"title":"Updated Gear","tags":["tools","printer"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", rec.Code, rec.Body.String())
	}
	sidecar, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", first.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), "Updated Gear") || !strings.Contains(string(sidecar), "printer") {
		t.Fatalf("sidecar not updated: %s", sidecar)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/models/"+first.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/files/"+second.ID+"/"+second.Files[0].ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Disposition") == "" {
		t.Fatalf("download status=%d disposition=%q", rec.Code, rec.Header().Get("Content-Disposition"))
	}

	req = httptest.NewRequest(http.MethodGet, "/mesh/"+second.ID+"/"+second.Files[0].ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("mesh status=%d len=%d", rec.Code, rec.Body.Len())
	}
}

func uploadSTLModel(t *testing.T, app *App, cookie *http.Cookie, name, readme string) Model {
	t.Helper()
	body, contentType := multipartZip(t, map[string]string{name: `solid p
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 1 0 0
vertex 0 1 0
endloop
endfacet
endsolid p`, "README.txt": readme})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	var m Model
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
