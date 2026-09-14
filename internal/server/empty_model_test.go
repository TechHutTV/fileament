package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDeletingLastVariantKeepsEmptyModelUsable(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "last.stl", "Empty model")
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := jsonReq(method, path, body)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		return rec
	}
	shareResponse := request(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+model.ID+`"}`)
	var share ShareLink
	if err := json.Unmarshal(shareResponse.Body.Bytes(), &share); err != nil || shareResponse.Code != http.StatusCreated {
		t.Fatalf("share status=%d err=%v", shareResponse.Code, err)
	}
	deleted := request(http.MethodDelete, "/api/models/"+model.ID+"/files/"+model.Files[0].ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	assertEmptyFiles(t, deleted.Body.Bytes(), model.ID)
	for _, path := range []string{"/api/models", "/api/models/" + model.ID, "/api/public/" + share.Token} {
		response := request(http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, response.Code)
		}
		assertEmptyFiles(t, response.Body.Bytes(), model.ID)
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	cfg, web := app.cfg, app.webFS
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(cfg.DataDir, "fileament.db") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	rebuilt, err := New(cfg, web)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	restored, err := rebuilt.getModel(model.ID)
	if err != nil || len(restored.Files) != 0 {
		t.Fatalf("rebuilt model=%+v err=%v", restored, err)
	}
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyFiles(t, encoded, model.ID)
}

func assertEmptyFiles(t *testing.T, data []byte, modelID string) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	var visit func(any)
	visit = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if value["id"] == modelID {
				found = true
				files, ok := value["files"].([]any)
				if !ok || len(files) != 0 {
					t.Fatalf("files must be an empty JSON array: %s", data)
				}
			}
			for _, child := range value {
				visit(child)
			}
		case []any:
			for _, child := range value {
				visit(child)
			}
		}
	}
	visit(decoded)
	if !found {
		t.Fatalf("model missing from response: %s", data)
	}
}
