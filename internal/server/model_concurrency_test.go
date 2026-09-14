package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestConcurrentSidecarWritesStayAtomic(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 24)
	for i := 0; i < cap(errors); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			next := model
			next.Title = fmt.Sprintf("Title %d", index)
			errors <- app.writeSidecar(next)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("concurrent write: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored Model
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("malformed sidecar: %v", err)
	}
	if stored.ID != model.ID || !reflect.DeepEqual(stored.Files, model.Files) {
		t.Fatalf("sidecar changed unrelated data: %+v", stored)
	}
	matches, err := filepath.Glob(filepath.Join(app.cfg.DataDir, "models", model.ID, "*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary sidecars remain: %v, %v", matches, err)
	}
}

func TestConcurrentModelMutationsPreserveAllDurableState(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	body, contentType := multipartZip(t, map[string]string{"keep.stl": validSTL(), "remove.stl": validSTL(), "keep.png": "image one", "remove.png": "image two"})
	create := httptest.NewRequest(http.MethodPost, "/api/models", body)
	create.Header.Set("Content-Type", contentType)
	create.AddCookie(cookie)
	created := httptest.NewRecorder()
	app.Router().ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	var model Model
	if err := json.Unmarshal(created.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	var requests []*http.Request
	for _, patch := range []string{`{"title":"Updated title"}`, `{"description":"Updated description"}`, `{"author":"Owner"}`, `{"license":"CC0"}`, `{"sourceUrl":"https://example.com/model"}`, `{"tags":["durable"]}`} {
		requests = append(requests, jsonReq(http.MethodPatch, "/api/models/"+model.ID, patch))
	}
	requests = append(requests, httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/files/"+model.Files[1].ID, nil), httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/images/"+model.Images[1].ID, nil))
	body, contentType = multipartZip(t, map[string]string{"added.stl": validSTL(), "added.png": "image three"})
	appendReq := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/files", body)
	appendReq.Header.Set("Content-Type", contentType)
	requests = append(requests, appendReq)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan string, len(requests))
	for _, req := range requests {
		req.AddCookie(cookie)
		wg.Add(1)
		go func(req *http.Request) {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code < 200 || rec.Code >= 300 {
				errors <- fmt.Sprintf("%s %s: %d %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
			}
		}(req)
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; _ = app.processNextThumbnail() }()
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	stored, err := app.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Title != "Updated title" || stored.Description != "Updated description" || stored.Author != "Owner" || stored.License != "CC0" || stored.SourceURL != "https://example.com/model" || !reflect.DeepEqual(stored.Tags, []string{"durable"}) {
		t.Fatalf("a concurrent metadata patch was lost: %+v", stored)
	}
	if len(stored.Files) != 2 || len(stored.Images) != 2 {
		t.Fatalf("files=%d images=%d", len(stored.Files), len(stored.Images))
	}
	data, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar Model
	if err := json.Unmarshal(data, &sidecar); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, sidecar) {
		t.Fatalf("database and sidecar differ: database=%+v sidecar=%+v", stored, sidecar)
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	cfg, web := app.cfg, app.webFS
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-shm", "-wal"} {
		if err := os.Remove(filepath.Join(cfg.DataDir, "fileament.db") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	rebuilt, err := New(cfg, web)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	got, err := rebuilt.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(stored)
	after, _ := json.Marshal(got)
	if !bytes.Equal(before, after) {
		t.Fatalf("rebuilt model differs: before=%s after=%s", before, after)
	}
}
