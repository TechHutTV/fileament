package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestContainedPathsAtFilesystemRoot(t *testing.T) {
	for _, root := range []string{string(filepath.Separator), t.TempDir()} {
		for _, rel := range []string{"part.stl", "files/part.stl", "files/../part.stl"} {
			got, err := containedPath(root, rel)
			if want := filepath.Join(root, filepath.FromSlash(rel)); err != nil || got != want {
				t.Errorf("containedPath(%q, %q) = %q, %v; want %q", root, rel, got, err, want)
			}
		}
		for _, rel := range []string{"", ".", "..", "../part.stl", "files/../../part.stl", "/part.stl", "part\x00.stl"} {
			if got, err := containedPath(root, rel); !errors.Is(err, errInvalidPath) || got != "" {
				t.Errorf("containedPath(%q, %q) = %q, %v; want invalid path", root, rel, got, err)
			}
		}
		if got, err := containedName(root, "part.stl"); err != nil || got != filepath.Join(root, "part.stl") {
			t.Errorf("containedName(%q, part.stl) = %q, %v", root, got, err)
		}
		for _, name := range []string{"", ".", "..", "../part.stl", "files/part.stl", `files\part.stl`, "/part.stl", "part\x00.stl"} {
			if got, err := containedName(root, name); !errors.Is(err, errInvalidPath) || got != "" {
				t.Errorf("containedName(%q, %q) = %q, %v; want invalid path", root, name, got, err)
			}
		}
	}
}

func TestUploadAndRebuildFromFilesystemRoot(t *testing.T) {
	t.Chdir(string(filepath.Separator))
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Root directory upload")
	if err := validateSidecarModel(model); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(app.cfg.DataDir, "fileament.db")); err != nil {
		t.Fatal(err)
	}
	rebuilt := newTestAppWithConfig(t, app.cfg)
	got, err := rebuilt.getModel(model.ID)
	if err != nil || got.Title != model.Title || len(got.Files) != 1 || got.Files[0].Filename != "part.stl" {
		t.Fatalf("rebuilt upload = %+v, %v", got, err)
	}
}
