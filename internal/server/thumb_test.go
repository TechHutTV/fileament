package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbnailJobRendersPNGAndSendsEvent(t *testing.T) {
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
		if evt.ThumbPath != "thumbs/"+model.Files[0].ID+".png" {
			t.Fatalf("event thumbnail path = %q", evt.ThumbPath)
		}
	default:
		t.Fatal("expected thumbnail event")
	}
	thumb := filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", model.Files[0].ID+".png")
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("thumb missing: %v", err)
	}
	var status, fileThumb, primary string
	err := app.db.QueryRow(`SELECT j.status, f.thumb_path, m.primary_thumb FROM jobs j JOIN files f ON f.id = j.file_id JOIN models m ON m.id = f.model_id WHERE f.id = ?`, model.Files[0].ID).Scan(&status, &fileThumb, &primary)
	if err != nil {
		t.Fatal(err)
	}
	if status != "done" || fileThumb != "thumbs/"+model.Files[0].ID+".png" || primary != "card.png" {
		t.Fatalf("unexpected job/thumb state status=%s file=%s primary=%s", status, fileThumb, primary)
	}
}

func TestSetThumbnailPreservesGeneratedPNGFormat(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "selected.stl", "Selected")
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	req := jsonReq(http.MethodPut, "/api/models/"+model.ID+"/thumb", `{"fileId":"`+model.Files[0].ID+`"}`)
	req.AddCookie(loginCookie(t, app, "password-password"))
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set thumbnail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated Model
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.PrimaryThumb != "card.png" {
		t.Fatalf("primary thumbnail = %q, want card.png", updated.PrimaryThumb)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", "card.png")); err != nil {
		t.Fatalf("PNG card thumbnail missing: %v", err)
	}
}

func TestDeletingSelectedVariantClearsPrimaryThumbnail(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "base.stl", "Variants")
	body, contentType := multipartFile(t, "selected.stl", []byte(validSTL()))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/files", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add variant status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for range model.Files {
		if err := app.processNextThumbnail(); err != nil {
			t.Fatal(err)
		}
	}
	selected := model.Files[1]
	req = jsonReq(http.MethodPut, "/api/models/"+model.ID+"/thumb", `{"fileId":"`+selected.ID+`"}`)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set thumbnail status = %d body=%s", rec.Code, rec.Body.String())
	}
	selectedThumb := filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", selected.ID+".png")
	if err := os.WriteFile(selectedThumb, []byte("regenerated thumbnail"), 0o644); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/files/"+selected.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete selected variant status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated Model
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.PrimaryThumb != "" || len(updated.Files) != 1 || updated.Files[0].ID == selected.ID {
		t.Fatalf("unexpected model after deleting selected variant: %#v", updated)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", "card.png")); !os.IsNotExist(err) {
		t.Fatalf("stale card thumbnail remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", primaryThumbSourceName)); !os.IsNotExist(err) {
		t.Fatalf("stale primary thumbnail source remains: %v", err)
	}
	sidecarData, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar Model
	if err := json.Unmarshal(sidecarData, &sidecar); err != nil {
		t.Fatal(err)
	}
	if sidecar.PrimaryThumb != "" || len(sidecar.Files) != 1 {
		t.Fatalf("stale sidecar after deleting selected variant: %#v", sidecar)
	}
}

func TestSetThumbnailRejectsStoredPathOutsideThumbnailDirectory(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "selected.stl", "Selected")
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(app.cfg.DataDir, "models", "outside.png")
	if err := os.WriteFile(outside, []byte("not a thumbnail"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE files SET thumb_path = '../outside.png' WHERE id = ?`, model.Files[0].ID); err != nil {
		t.Fatal(err)
	}
	req := jsonReq(http.MethodPut, "/api/models/"+model.ID+"/thumb", `{"fileId":"`+model.Files[0].ID+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("set thumbnail status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestThumbnailRenderUpgradeRequeuesExistingFiles(t *testing.T) {
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
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs")
	legacyFile := filepath.Join(thumbDir, model.Files[0].ID+".jpg")
	legacyCard := filepath.Join(thumbDir, "card.jpg")
	if err := copyFile(legacyFile, filepath.Join(thumbDir, model.Files[0].ID+".png")); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(legacyCard, filepath.Join(thumbDir, "card.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE files SET thumb_path = ? WHERE id = ?`, "thumbs/"+model.Files[0].ID+".jpg", model.Files[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE models SET primary_thumb = 'card.jpg' WHERE id = ?`, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(thumbDir, model.Files[0].ID+".png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(thumbDir, "card.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE settings SET value = '2' WHERE key = ?`, thumbnailRenderVersionKey); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshThumbnailRenderVersion(); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = 'thumbnail' AND status = 'pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	var fileThumb, primary, version string
	if err := app.db.QueryRow(`SELECT COALESCE(f.thumb_path, ''), COALESCE(m.primary_thumb, '') FROM files f JOIN models m ON m.id = f.model_id LIMIT 1`).Scan(&fileThumb, &primary); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, thumbnailRenderVersionKey).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || fileThumb != "thumbs/"+model.Files[0].ID+".jpg" || primary != "card.jpg" || version != thumbnailRenderVersion {
		t.Fatalf("unexpected refresh state pending=%d file=%q primary=%q version=%q", pending, fileThumb, primary, version)
	}
	if err := app.refreshThumbnailRenderVersion(); err != nil {
		t.Fatal(err)
	}
	var pendingAfterRestart int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = 'thumbnail' AND status = 'pending'`).Scan(&pendingAfterRestart); err != nil {
		t.Fatal(err)
	}
	if pendingAfterRestart != pending {
		t.Fatalf("same-version restart changed pending jobs from %d to %d", pending, pendingAfterRestart)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	if err := app.db.QueryRow(`SELECT f.thumb_path, m.primary_thumb FROM files f JOIN models m ON m.id = f.model_id LIMIT 1`).Scan(&fileThumb, &primary); err != nil {
		t.Fatal(err)
	}
	if fileThumb != "thumbs/"+model.Files[0].ID+".png" || primary != "card.png" {
		t.Fatalf("upgraded thumbnail paths file=%q primary=%q", fileThumb, primary)
	}
	for _, legacy := range []string{legacyFile, legacyCard} {
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy thumbnail still exists at %s", legacy)
		}
	}
}

func TestThumbnailRenderUpgradePreservesSelectedCard(t *testing.T) {
	app := newAuthedTestApp(t)
	body, contentType := multipartZip(t, map[string]string{
		"small.stl": `solid small
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 1 0 0
vertex 0 1 0
endloop
endfacet
endsolid small`,
		"large.stl": `solid large
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 4 0 0
vertex 0 2 2
endloop
endfacet
facet normal 1 0 0
outer loop
vertex 0 0 0
vertex 0 2 2
vertex 0 0 3
endloop
endfacet
endsolid large`,
	})
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
	for range model.Files {
		if err := app.processNextThumbnail(); err != nil {
			t.Fatal(err)
		}
	}
	if len(model.Files) != 2 {
		t.Fatalf("model files = %d, want 2", len(model.Files))
	}
	selected := model.Files[0]
	for _, file := range model.Files[1:] {
		if file.SizeBytes < selected.SizeBytes {
			selected = file
		}
	}
	thumbDir := filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs")
	selectedPNG := filepath.Join(thumbDir, selected.ID+".png")
	if err := copyFile(filepath.Join(thumbDir, "card.png"), selectedPNG); err != nil {
		t.Fatal(err)
	}
	for _, file := range model.Files {
		pngPath := filepath.Join(thumbDir, file.ID+".png")
		jpgPath := filepath.Join(thumbDir, file.ID+".jpg")
		if err := copyFile(jpgPath, pngPath); err != nil {
			t.Fatal(err)
		}
		if _, err := app.db.Exec(`UPDATE files SET thumb_path = ? WHERE id = ?`, "thumbs/"+file.ID+".jpg", file.ID); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(pngPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyFile(filepath.Join(thumbDir, "card.jpg"), filepath.Join(thumbDir, "card.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(thumbDir, "card.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE models SET primary_thumb = 'card.jpg' WHERE id = ?`, model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE settings SET value = '2' WHERE key = ?`, thumbnailRenderVersionKey); err != nil {
		t.Fatal(err)
	}
	if err := app.refreshThumbnailRenderVersion(); err != nil {
		t.Fatal(err)
	}
	for range model.Files {
		if err := app.processNextThumbnail(); err != nil {
			t.Fatal(err)
		}
	}
	card, err := os.ReadFile(filepath.Join(thumbDir, "card.png"))
	if err != nil {
		t.Fatal(err)
	}
	selectedThumb, err := os.ReadFile(selectedPNG)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(card, selectedThumb) {
		t.Fatal("renderer upgrade replaced the selected card thumbnail")
	}
}
