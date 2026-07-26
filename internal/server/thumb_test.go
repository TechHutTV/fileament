package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbnailJobRendersJPEGAndSendsEvent(t *testing.T) {
	app := newAuthedTestApp(t)
	body, contentType := multipartZip(t, map[string]string{"part.stl": `solid p
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 2 0 0
vertex 0 2 0
endloop
endfacet
endsolid p`})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(loginCookie(t, app, "password-password"))
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	events := app.subscribeEvents()
	defer app.unsubscribeEvents(events)
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	select {
	case evt := <-events:
		if evt.ModelID != model.ID || evt.FileID != model.Files[0].ID {
			t.Fatalf("unexpected event %#v", evt)
		}
	default:
		t.Fatal("expected thumbnail event")
	}
	thumb := filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", model.Files[0].ID+".jpg")
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("thumb missing: %v", err)
	}
	var status, fileThumb, primary string
	err := app.db.QueryRow(`SELECT j.status, f.thumb_path, m.primary_thumb FROM jobs j JOIN files f ON f.id = j.file_id JOIN models m ON m.id = f.model_id WHERE f.id = ?`, model.Files[0].ID).Scan(&status, &fileThumb, &primary)
	if err != nil {
		t.Fatal(err)
	}
	if status != "done" || fileThumb == "" || primary != "card.jpg" {
		t.Fatalf("unexpected job/thumb state status=%s file=%s primary=%s", status, fileThumb, primary)
	}
}
