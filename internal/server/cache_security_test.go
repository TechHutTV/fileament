package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestSensitiveResponsesCannotBeStored(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	body, contentType := multipartZip(t, map[string]string{"part.stl": validSTL(), "photo.png": "image"})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload=%d %s", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	share := createCacheTestShare(t, app, cookie, model.ID)
	public := "/api/public/" + share.Token
	paths := []string{
		"/api/me", "/api/models", "/api/models/" + model.ID, "/api/collections", "/api/shares", "/api/storage",
		"/files/" + model.ID + "/" + model.Files[0].ID,
		"/mesh/" + model.ID + "/" + model.Files[0].ID,
		"/images/" + model.ID + "/" + model.Images[0].ID,
		"/thumbs/" + model.ID + "/card.png",
		public, public + "/status", public + "/files/" + model.Files[0].ID,
		public + "/mesh/" + model.Files[0].ID, public + "/thumbs/card.png",
		public + "/images/" + model.Images[0].ID, "/s/" + share.Token,
		"/api/public/invalid/status", "/files/invalid/invalid",
	}
	for _, path := range paths {
		for _, authorized := range []bool{true, false} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if authorized {
				req.AddCookie(cookie)
			}
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("%s authorized=%v status=%d cache=%q", path, authorized, rec.Code, got)
			}
		}
	}
}

func TestCachingProxyRechecksShareAfterRevocation(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Part")
	share := createCacheTestShare(t, app, cookie, model.ID)
	origin := httptest.NewServer(app.Router())
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(originURL)
	var mu sync.Mutex
	var cached []byte
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode == http.StatusOK && !strings.Contains(response.Header.Get("Cache-Control"), "private") && !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
			data, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if err := response.Body.Close(); err != nil {
				return err
			}
			response.Body = io.NopCloser(bytes.NewReader(data))
			mu.Lock()
			cached = data
			mu.Unlock()
		}
		return nil
	}
	cache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		data := cached
		mu.Unlock()
		if data != nil {
			_, _ = w.Write(data)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer cache.Close()
	path := cache.URL + "/api/public/" + share.Token + "/mesh/" + model.Files[0].ID
	response, err := http.Get(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := response.Header.Get("Last-Modified")
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initial download=%d", response.StatusCode)
	}
	revoke := httptest.NewRequest(http.MethodDelete, "/api/shares/"+share.ID, nil)
	revoke.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke=%d", rec.Code)
	}
	conditional, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	conditional.Header.Set("If-Modified-Since", modified)
	response, err = http.DefaultClient.Do(conditional)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGone {
		t.Fatalf("proxy served revoked content: status=%d", response.StatusCode)
	}
}

func createCacheTestShare(t *testing.T, app *App, cookie *http.Cookie, modelID string) ShareLink {
	t.Helper()
	req := jsonReq(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+modelID+`"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("share=%d %s", rec.Code, rec.Body.String())
	}
	var share ShareLink
	if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
		t.Fatal(err)
	}
	return share
}
