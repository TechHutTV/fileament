package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
	"github.com/go-chi/chi/v5"
)

const (
	backupFormatVersion = 1
	dataFormatVersion   = 1
)

type backupManifest struct {
	BackupFormatVersion int    `json:"backupFormatVersion"`
	DataFormatVersion   int    `json:"dataFormatVersion"`
	DatabaseVersion     int    `json:"databaseVersion"`
	CreatedAt           string `json:"createdAt"`
	Models              int    `json:"models"`
	Files               int    `json:"files"`
	Collections         int    `json:"collections"`
}

func (a *App) mountBackupRoutes(r chi.Router) {
	r.With(a.requireDataAuth).Post("/api/backups", a.handleCreateBackup)
	r.With(a.requireDataAuth).Post("/api/backups/inspect", a.handleInspectBackup)
	r.With(a.requireDataAuth).Post("/api/backups/restore", a.handleApplyRestore)
}

func (a *App) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	a.dataMu.Lock()
	if !a.validSession(r) {
		a.dataMu.Unlock()
		writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	path, manifest, err := a.createBackupArchive(filepath.Join(a.cfg.DataDir, "tmp", "backups"))
	a.dataMu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer os.Remove(path)

	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	filename := "fileament-backup-" + strings.NewReplacer(":", "", "-", "").Replace(manifest.CreatedAt) + ".fileament"
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeContent(w, r, filename, stat.ModTime(), file)
}

func (a *App) createBackupArchive(dir string) (string, backupManifest, error) {
	var manifest backupManifest
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", manifest, err
	}
	id := ids.New()
	snapshotPath := filepath.Join(dir, id+".db")
	archivePath := filepath.Join(dir, id+".fileament")
	defer os.Remove(snapshotPath)

	if _, err := a.db.Exec(`VACUUM INTO '` + strings.ReplaceAll(filepath.ToSlash(snapshotPath), "'", "''") + `'`); err != nil {
		return "", manifest, err
	}
	snapshot, err := sql.Open("sqlite", "file:"+filepath.ToSlash(snapshotPath))
	if err != nil {
		return "", manifest, err
	}
	if _, err := snapshot.Exec(`DELETE FROM sessions`); err != nil {
		_ = snapshot.Close()
		return "", manifest, err
	}
	if _, err := snapshot.Exec(`UPDATE jobs SET status = 'pending', error = NULL WHERE status = 'running'`); err != nil {
		_ = snapshot.Close()
		return "", manifest, err
	}
	if err := snapshot.Close(); err != nil {
		return "", manifest, err
	}

	manifest = backupManifest{
		BackupFormatVersion: backupFormatVersion,
		DataFormatVersion:   dataFormatVersion,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	if err := a.db.QueryRow(`PRAGMA user_version`).Scan(&manifest.DatabaseVersion); err != nil {
		return "", manifest, err
	}
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM models`:      &manifest.Models,
		`SELECT COUNT(*) FROM files`:       &manifest.Files,
		`SELECT COUNT(*) FROM collections`: &manifest.Collections,
	} {
		if err := a.db.QueryRow(query).Scan(target); err != nil {
			return "", manifest, err
		}
	}

	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", manifest, err
	}
	zw := zip.NewWriter(archive)
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(archivePath)
		}
	}()
	manifestEntry, err := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate})
	if err == nil {
		err = json.NewEncoder(manifestEntry).Encode(manifest)
	}
	if err == nil {
		err = addFileToBackup(zw, snapshotPath, "data/fileament.db")
	}
	if err == nil {
		err = addPersistentDataToBackup(zw, a.cfg.DataDir)
	}
	if closeErr := zw.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", manifest, err
	}
	failed = false
	return archivePath, manifest, nil
}

func addPersistentDataToBackup(zw *zip.Writer, root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	excluded := map[string]bool{
		"fileament.db":         true,
		"fileament.db-journal": true,
		"fileament.db-shm":     true,
		"fileament.db-wal":     true,
		"tmp":                  true,
		"backups":              true,
		".restore":             true,
	}
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		path := filepath.Join(root, entry.Name())
		err := filepath.WalkDir(path, func(current string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("backup does not support symbolic links")
			}
			rel, err := filepath.Rel(root, current)
			if err != nil {
				return err
			}
			name := "data/" + filepath.ToSlash(rel)
			if d.IsDir() {
				_, err = zw.CreateHeader(&zip.FileHeader{Name: strings.TrimSuffix(name, "/") + "/", Method: zip.Store})
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("backup contains an unsupported file type")
			}
			return addFileToBackup(zw, current, name)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func addFileToBackup(zw *zip.Writer, path, name string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(name)
	header.Method = zip.Deflate
	entry, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(entry, file)
	return err
}
