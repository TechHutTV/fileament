package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestRenameModelFileIsDurable(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "gear.stl", "Gear model")
	file := model.Files[0]
	if _, err := app.db.Exec(`CREATE TRIGGER preserve_model_change_during_file_rename AFTER UPDATE OF filename ON files BEGIN UPDATE models SET author = 'Preserved Author' WHERE id = NEW.model_id; END`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/models/"+model.ID+"/files/"+file.ID, strings.NewReader(`{"filename":"drive-gear.stl"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renamed Model
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil {
		t.Fatal(err)
	}
	if len(renamed.Files) != 1 || renamed.Files[0].Filename != "drive-gear.stl" || renamed.Files[0].RelPath != file.RelPath || renamed.Author != "Preserved Author" {
		t.Fatalf("unexpected renamed model %#v", renamed)
	}

	req = httptest.NewRequest(http.MethodGet, "/files/"+model.ID+"/"+file.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), `filename="drive-gear.stl"`) {
		t.Fatalf("download status=%d disposition=%q", rec.Code, rec.Header().Get("Content-Disposition"))
	}

	sidecar, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"filename": "drive-gear.stl"`) || !strings.Contains(string(sidecar), `"author": "Preserved Author"`) {
		t.Fatalf("renamed filename missing from sidecar: %s", sidecar)
	}

	cfg := app.cfg
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-shm", "-wal"} {
		if err := os.Remove(filepath.Join(cfg.DataDir, "fileament.db") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	rebuilt, err := New(cfg, os.DirFS(cfg.WebDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rebuilt.Close() })
	recovered, err := rebuilt.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Files) != 1 || recovered.Files[0].Filename != "drive-gear.stl" || recovered.Files[0].RelPath != file.RelPath || recovered.Author != "Preserved Author" {
		t.Fatalf("unexpected rebuilt model %#v", recovered)
	}
}

func TestRenameModelFileRejectsUnsafeNames(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	cases := []struct {
		name     string
		filename string
	}{
		{"empty", ""},
		{"extension only", ".stl"},
		{"traversal", "../gear.stl"},
		{"wrong extension", "gear.obj"},
		{"control character", "gear\r\n.stl"},
		{"quote", `gear".stl`},
		{"too long", strings.Repeat("a", 252) + ".stl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := uploadSTLModel(t, app, cookie, "gear.stl", "Gear model")
			body, err := json.Marshal(map[string]string{"filename": tc.filename})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/models/"+model.ID+"/files/"+model.Files[0].ID, strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
			}
			stored, err := app.getModel(model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Files[0].Filename != "gear.stl" {
				t.Fatalf("unsafe filename persisted as %q", stored.Files[0].Filename)
			}
		})
	}
}

func TestCatalogPaginationUsesTheSelectedSortKey(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	models := []Model{
		uploadSTLModel(t, app, cookie, "zulu.stl", "Zulu"),
		uploadSTLModel(t, app, cookie, "alpha.stl", "Alpha"),
		uploadSTLModel(t, app, cookie, "mike.stl", "Mike"),
	}
	values := []struct {
		title                  string
		created, updated, size int64
	}{{"Zulu", 100, 300, 100}, {"Alpha", 200, 100, 300}, {"Mike", 300, 200, 200}}
	for i, model := range models {
		v := values[i]
		if _, err := app.db.Exec(`UPDATE models SET title = ?, created_at = ?, updated_at = ?, total_bytes = ? WHERE id = ?`, v.title, v.created, v.updated, v.size, model.ID); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]string{
		"title":   {"Alpha", "Mike", "Zulu"},
		"updated": {"Zulu", "Mike", "Alpha"},
		"size":    {"Alpha", "Mike", "Zulu"},
	}
	for sortKey, want := range cases {
		t.Run(sortKey, func(t *testing.T) {
			cursor := ""
			var got []string
			for {
				path := "/api/models?limit=1&sort=" + sortKey
				if cursor != "" {
					path += "&cursor=" + url.QueryEscape(cursor)
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				app.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var page struct {
					Items      []Model `json:"items"`
					NextCursor string  `json:"nextCursor"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					got = append(got, item.Title)
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
				if len(got) > len(want) {
					t.Fatal("pagination did not terminate")
				}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("titles=%v want=%v", got, want)
			}
		})
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
