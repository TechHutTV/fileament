package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
)

type assetFixture struct {
	app    *App
	cookie *http.Cookie
	model  Model
	share  ShareLink
}

type assetRequest struct {
	name, path, rel string
	public          bool
}

func newAssetFixture(t *testing.T) assetFixture {
	t.Helper()
	return newAssetFixtureWithApp(t, newAuthedTestApp(t))
}

func newAssetFixtureWithApp(t *testing.T, app *App) assetFixture {
	t.Helper()
	cookie := loginCookie(t, app, "password-password")
	body, contentType := multipartZip(t, map[string]string{"part.stl": validSTL(), "photo.png": string(pngBytes(t))})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	rec := serveMutationRequest(app, cookie, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("asset fixture upload: %d", rec.Code)
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	model, err := app.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec = serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+model.ID+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("asset fixture share: %d", rec.Code)
	}
	var share ShareLink
	if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
		t.Fatal(err)
	}
	return assetFixture{app, cookie, model, share}
}

func (f assetFixture) requests() []assetRequest {
	model, file, image := f.model.ID, f.model.Files[0], f.model.Images[0]
	public := "/api/public/" + f.share.Token
	return []assetRequest{
		{"owner download", "/files/" + model + "/" + file.ID, file.RelPath, false},
		{"owner mesh", "/mesh/" + model + "/" + file.ID, file.RelPath, false},
		{"owner image", "/images/" + model + "/" + image.ID, image.RelPath, false},
		{"owner thumbnail", "/thumbs/" + model + "/" + f.model.PrimaryThumb, "thumbs/" + f.model.PrimaryThumb, false},
		{"owner file thumbnail", "/thumbs/" + model + "/" + filepath.Base(file.ThumbPath), file.ThumbPath, false},
		{"public download", public + "/files/" + file.ID, file.RelPath, true},
		{"public mesh", public + "/mesh/" + file.ID, file.RelPath, true},
		{"public image", public + "/images/" + image.ID, image.RelPath, true},
		{"public thumbnail", public + "/thumbs/" + f.model.PrimaryThumb, "thumbs/" + f.model.PrimaryThumb, true},
		{"public file thumbnail", public + "/thumbs/" + filepath.Base(file.ThumbPath), file.ThumbPath, true},
	}
}

func (f assetFixture) request(asset assetRequest, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, asset.path, nil)
	if !asset.public {
		req.AddCookie(f.cookie)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	f.app.Router().ServeHTTP(rec, req)
	return rec
}

func TestModelAssetsRejectSymlinkFilesAndDirectories(t *testing.T) {
	f := newAssetFixture(t)
	modelRoot := filepath.Join(f.app.cfg.DataDir, "models", f.model.ID)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, asset := range f.requests() {
		t.Run(asset.name, func(t *testing.T) {
			path := filepath.Join(modelRoot, asset.rel)
			saved := path + ".saved"
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.RemoveAll(path)
				if err := os.Rename(saved, path); err != nil {
					t.Error(err)
				}
			})
			relative, err := filepath.Rel(filepath.Dir(path), outside)
			if err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{outside, relative, filepath.Base(saved)} {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				rec := f.request(asset, nil)
				if rec.Code != http.StatusNotFound {
					t.Errorf("symlink returned status %d", rec.Code)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if rec := f.request(asset, nil); rec.Code != http.StatusNotFound {
				t.Errorf("directory returned status %d", rec.Code)
			}
		})
	}
}

func TestModelAssetsRejectSymlinkAncestors(t *testing.T) {
	f := newAssetFixture(t)
	for _, component := range []string{"models", "models/" + f.model.ID, "models/" + f.model.ID + "/files", "models/" + f.model.ID + "/images", "models/" + f.model.ID + "/thumbs"} {
		t.Run(component, func(t *testing.T) {
			path := filepath.Join(f.app.cfg.DataDir, component)
			saved := path + ".saved"
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Remove(path)
				if err := os.Rename(saved, path); err != nil {
					t.Error(err)
				}
			})
			if err := os.Symlink(filepath.Base(saved), path); err != nil {
				t.Fatal(err)
			}
			for _, asset := range f.requests() {
				if !strings.HasPrefix(filepath.ToSlash(filepath.Join("models", f.model.ID, asset.rel)), component+"/") {
					continue
				}
				if rec := f.request(asset, nil); rec.Code != http.StatusNotFound {
					t.Errorf("%s followed a directory symlink: %d", asset.name, rec.Code)
				}
			}
		})
	}
}

func TestModelAssetsPreserveRangesAndConditionalRequests(t *testing.T) {
	f := newAssetFixture(t)
	for _, asset := range f.requests() {
		t.Run(asset.name, func(t *testing.T) {
			full := f.request(asset, nil)
			if full.Code != http.StatusOK || full.Body.Len() < 8 {
				t.Fatalf("ordinary read: %d, %d bytes", full.Code, full.Body.Len())
			}
			part := f.request(asset, map[string]string{"Range": "bytes=1-7"})
			if part.Code != http.StatusPartialContent || part.Body.String() != full.Body.String()[1:8] {
				t.Fatalf("range read: %d", part.Code)
			}
			if since := full.Header().Get("Last-Modified"); since == "" {
				t.Fatal("missing Last-Modified")
			} else if rec := f.request(asset, map[string]string{"If-Modified-Since": since}); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
				t.Fatalf("conditional read: %d", rec.Code)
			}
			if full.Header().Get("Cache-Control") != "private, no-store" || asset.public && full.Header().Get("X-Robots-Tag") != "noindex" {
				t.Fatal("asset privacy headers changed")
			}
			if strings.HasPrefix(asset.rel, "files/") {
				if full.Header().Get("Content-Type") != "application/octet-stream" || full.Header().Get("X-Content-Type-Options") != "nosniff" || full.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
					t.Fatal("mesh response protections changed")
				}
				if strings.Contains(asset.name, "download") && !strings.HasPrefix(full.Header().Get("Content-Disposition"), "attachment;") {
					t.Fatal("missing attachment disposition")
				}
			} else if !strings.HasPrefix(full.Header().Get("Content-Type"), "image/") {
				t.Fatal("image content type changed")
			}
		})
	}
}

func TestModelAssetsWithTrustedDataRoot(t *testing.T) {
	for _, mode := range []string{"relative", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if mode == "symlink" {
				alias := filepath.Join(t.TempDir(), "data")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				dir = alias
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				dir, err = filepath.Rel(cwd, dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			app := newTestAppWithConfig(t, config.Config{DataDir: dir, OwnerPassword: "password-password", MaxUploadMB: 32, ThumbWorkers: 0})
			f := newAssetFixtureWithApp(t, app)
			for _, asset := range f.requests() {
				if rec := f.request(asset, nil); rec.Code != http.StatusOK {
					t.Fatalf("%s: %d", asset.name, rec.Code)
				}
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := app.dataRoot.Stat("."); err == nil {
				t.Fatal("data root remained open after shutdown")
			}
		})
	}
}

func TestModelAssetsAfterRestore(t *testing.T) {
	source := newAssetFixture(t)
	backup := downloadBackup(t, source.app, source.cookie)
	destination := newAuthedTestApp(t)
	cookie := loginCookie(t, destination, "password-password")
	uploadSTLModel(t, destination, cookie, "old.stl", "Old model")
	inspection := inspectBackup(t, destination, cookie, backup)
	rec := serveMutationRequest(destination, cookie, jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: %d", rec.Code)
	}
	restored := source
	restored.app = destination
	restored.cookie = loginCookie(t, destination, "password-password")
	for _, asset := range source.requests() {
		before, after := source.request(asset, nil), restored.request(asset, nil)
		if before.Code != http.StatusOK || after.Code != http.StatusOK || before.Body.String() != after.Body.String() {
			t.Errorf("%s: source=%d restored=%d, restored bytes do not match", asset.name, before.Code, after.Code)
		}
	}
}
