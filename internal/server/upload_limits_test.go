package server

import (
	"archive/zip"
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestZIPRejectsExpandedSizeBeforeSignedConversion(t *testing.T) {
	for _, size := range []uint64{(1 << 20) - 10, (1 << 20) + 1, math.MaxInt64, 1 << 63, math.MaxUint64} {
		t.Run(strconv.FormatUint(size, 10), func(t *testing.T) {
			app := newAuthedTestApp(t)
			app.cfg.MaxUploadMB = 1
			var archive bytes.Buffer
			zw := zip.NewWriter(&archive)
			if _, err := zw.CreateRaw(&zip.FileHeader{Name: "extra.bin", Method: zip.Store, UncompressedSize64: size}); err != nil {
				t.Fatal(err)
			}
			mesh, err := zw.Create("model.stl")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mesh.Write([]byte(validSTL())); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			body, contentType := multipartFile(t, "model.zip", archive.Bytes())
			req := httptest.NewRequest(http.MethodPost, "/api/models", body)
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(loginCookie(t, app, "password-password"))
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("oversized ZIP status=%d", rec.Code)
			}
			var models int
			if err := app.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&models); err != nil || models != 0 {
				t.Fatalf("rejected ZIP left models=%d error=%v", models, err)
			}
			for _, name := range []string{"tmp", "models"} {
				entries, err := os.ReadDir(filepath.Join(app.cfg.DataDir, name))
				if err != nil || len(entries) != 0 {
					t.Fatalf("rejected ZIP left %s entries=%d error=%v", name, len(entries), err)
				}
			}
		})
	}
}
