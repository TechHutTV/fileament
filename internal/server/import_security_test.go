package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/ids"
)

func TestRestoreRejectsTraversalFileIdentifier(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	const unsafeID = "../../../../escaped"
	if _, err := app.db.Exec(`UPDATE files SET id = ? WHERE id = ?`, unsafeID, model.Files[0].ID); err != nil {
		t.Fatal(err)
	}
	model.Files[0].ID = unsafeID
	writeImportTestSidecar(t, app, model)
	archive, _, err := app.createBackupArchive(filepath.Join(app.cfg.DataDir, "tmp", "backup"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectAndExtractBackup(archive, filepath.Join(t.TempDir(), "stage"), 32<<20); err == nil {
		t.Fatal("restore accepted a traversal file identifier")
	}
}

func TestStartupRejectsUnsafeSidecarMetadata(t *testing.T) {
	cases := map[string]func(*Model){
		"file traversal":      func(m *Model) { m.Files[0].ID = "../../../../escaped" },
		"file backslash":      func(m *Model) { m.Files[0].ID = `..\escaped` },
		"model mismatch":      func(m *Model) { m.ID = "../escaped" },
		"file ownership":      func(m *Model) { m.Files[0].ModelID = "other" },
		"mesh path":           func(m *Model) { m.Files[0].RelPath = "files/../../outside.stl" },
		"mesh directory":      func(m *Model) { m.Files[0].RelPath = "thumbs/part.stl" },
		"mesh format":         func(m *Model) { m.Files[0].Format = "html" },
		"thumbnail path":      func(m *Model) { m.Files[0].ThumbPath = "thumbs/../../outside.png" },
		"thumbnail ownership": func(m *Model) { m.Files[0].ThumbPath = "thumbs/other.png" },
		"primary thumbnail":   func(m *Model) { m.PrimaryThumb = "../outside.png" },
		"duplicate file":      func(m *Model) { m.Files = append(m.Files, m.Files[0]) },
		"image identifier":    func(m *Model) { m.Images = []Image{{ID: "../image", ModelID: m.ID, RelPath: "images/photo.png"}} },
		"image ownership":     func(m *Model) { m.Images = []Image{{ID: ids.New(), ModelID: "other", RelPath: "images/photo.png"}} },
		"image path": func(m *Model) {
			m.Images = []Image{{ID: ids.New(), ModelID: m.ID, RelPath: "images/../../outside.png"}}
		},
	}
	for name, mutate := range cases {
		for _, freshIndex := range []bool{false, true} {
			indexName := "existing index"
			if freshIndex {
				indexName = "fresh index"
			}
			t.Run(name+"/"+indexName, func(t *testing.T) {
				app := newAuthedTestApp(t)
				model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
				sidecarPath := filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json")
				mutate(&model)
				contents, err := json.Marshal(model)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sidecarPath, contents, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := app.Close(); err != nil {
					t.Fatal(err)
				}
				if freshIndex {
					if err := os.Remove(filepath.Join(app.cfg.DataDir, "fileament.db")); err != nil {
						t.Fatal(err)
					}
				}
				reopened, err := New(app.cfg, app.webFS)
				if err == nil {
					_ = reopened.Close()
					t.Fatal("startup accepted unsafe sidecar metadata")
				}
			})
		}
	}
}

func TestThumbnailWorkerRejectsTraversalWithoutTouchingOutsideFiles(t *testing.T) {
	outer := t.TempDir()
	app := newTestAppWithConfig(t, config.Config{DataDir: filepath.Join(outer, "data"), OwnerPassword: "password-password", MaxUploadMB: 32})
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	const unsafeID = "../../../../escaped"
	for _, query := range []string{`UPDATE files SET id = ? WHERE id = ?`, `UPDATE jobs SET file_id = ? WHERE file_id = ?`} {
		if _, err := app.db.Exec(query, unsafeID, model.Files[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(outer, "escaped.jpg")
	if err := os.WriteFile(legacy, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.processNextThumbnail(); err == nil {
		t.Error("worker accepted a traversal file identifier")
	}
	if _, err := os.Stat(filepath.Join(outer, "escaped.png")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worker wrote outside the data directory: %v", err)
	}
	if contents, err := os.ReadFile(legacy); err != nil || string(contents) != "keep" {
		t.Error("worker changed an outside legacy thumbnail")
	}
	var status string
	if err := app.db.QueryRow(`SELECT status FROM jobs WHERE file_id = ?`, unsafeID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("unsafe job status=%q error=%v", status, err)
	}
}

func TestSidecarImportCannotOverwriteAnotherModelsAssets(t *testing.T) {
	for _, asset := range []string{"file", "image"} {
		t.Run(asset, func(t *testing.T) {
			app := newAuthedTestApp(t)
			cookie := loginCookie(t, app, "password-password")
			owner := uploadSTLModel(t, app, cookie, "owner.stl", "Owner")
			other := uploadSTLModel(t, app, cookie, "other.stl", "Other")
			if asset == "file" {
				other.Files[0].ID = owner.Files[0].ID
			} else {
				imageID := ids.New()
				if _, err := app.db.Exec(`INSERT INTO images(id, model_id, rel_path, sort_order) VALUES(?, ?, 'images/owner.png', 0)`, imageID, owner.ID); err != nil {
					t.Fatal(err)
				}
				other.Images = []Image{{ID: imageID, ModelID: other.ID, RelPath: "images/other.png"}}
			}
			before, err := app.getModel(owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.upsertSidecarModel(other); err == nil {
				t.Error("import accepted an identifier belonging to another model")
			}
			after, err := app.getModel(owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(before)
			got, _ := json.Marshal(after)
			if string(got) != string(want) {
				t.Fatal("import changed another model's assets")
			}
		})
	}
}

func writeImportTestSidecar(t *testing.T, app *App, model Model) {
	t.Helper()
	contents, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
