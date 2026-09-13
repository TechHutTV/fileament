package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
)

type backupSnapshot struct {
	root     string
	manifest backupManifest
}

// The caller holds dataMu while capturing SQLite and immutable catalog files.
func (a *App) captureBackupSnapshot(ctx context.Context) (*backupSnapshot, error) {
	dir := filepath.Join(a.cfg.DataDir, "tmp", "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(dir, "snapshot-")
	if err != nil {
		return nil, err
	}
	snapshot := &backupSnapshot{root: root, manifest: backupManifest{BackupFormatVersion: backupFormatVersion, DataFormatVersion: dataFormatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339)}}
	max := a.maxBackupBytes()
	var pages, pageSize int64
	if err := a.db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return snapshot, err
	}
	if err := a.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return snapshot, err
	}
	if pageSize <= 0 || pages > max/pageSize {
		return snapshot, errBackupTooLarge
	}
	remaining, count := max-pages*pageSize, 2
	err = walkBackupData(ctx, a.cfg.DataDir, func(path, rel string, info fs.FileInfo) error {
		count++
		if count > maxBackupEntries || (!info.IsDir() && info.Size() > remaining) {
			return errBackupTooLarge
		}
		target := filepath.Join(root, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		remaining -= info.Size()
		immutable := rel == "collections.json" || strings.HasPrefix(filepath.ToSlash(rel), "models/")
		if immutable {
			if err := os.Link(path, target); err == nil {
				return nil
			}
		}
		return copyBackupSnapshotFile(ctx, path, target, info.Size())
	})
	if err != nil {
		return snapshot, err
	}
	snapshotPath := filepath.Join(root, "fileament.db")
	if _, err := a.db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(filepath.ToSlash(snapshotPath), "'", "''")+"'"); err != nil {
		return snapshot, err
	}
	db, err := openSQLite(snapshotPath, nil)
	if err != nil {
		return snapshot, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DELETE FROM sessions; UPDATE jobs SET status = 'pending', error = NULL WHERE status = 'running'"); err != nil {
		return snapshot, err
	}
	for query, target := range map[string]*int{
		"PRAGMA user_version":              &snapshot.manifest.DatabaseVersion,
		"SELECT COUNT(*) FROM models":      &snapshot.manifest.Models,
		"SELECT COUNT(*) FROM files":       &snapshot.manifest.Files,
		"SELECT COUNT(*) FROM collections": &snapshot.manifest.Collections,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return snapshot, err
		}
	}
	return snapshot, db.Close()
}

func copyBackupSnapshotFile(ctx context.Context, source, target string, size int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(&backupWriter{ctx: ctx, writer: output, remaining: size}, input)
	if copyErr == nil && n != size {
		copyErr = errors.New("backup source changed size")
	}
	return errors.Join(copyErr, output.Close())
}

func (a *App) createBackupArchive(dir string) (string, backupManifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), backupExportTimeout)
	defer cancel()
	snapshot, err := a.captureBackupSnapshot(ctx)
	if snapshot == nil {
		return "", backupManifest{}, err
	}
	defer os.RemoveAll(snapshot.root)
	if err != nil {
		return "", snapshot.manifest, err
	}
	path, err := a.archiveBackupSnapshot(ctx, snapshot, dir)
	return path, snapshot.manifest, err
}

func (a *App) archiveBackupSnapshot(ctx context.Context, snapshot *backupSnapshot, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ids.New()+".fileament")
	archive, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	zw := zip.NewWriter(&backupWriter{ctx: ctx, writer: archive, remaining: a.maxBackupBytes()})
	manifest, err := json.Marshal(snapshot.manifest)
	remaining := a.maxBackupBytes() - int64(len(manifest))
	if err == nil {
		var entry io.Writer
		entry, err = zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate})
		if err == nil {
			_, err = entry.Write(manifest)
		}
	}
	if err == nil {
		err = filepath.WalkDir(snapshot.root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || path == snapshot.root {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(snapshot.root, path)
			if err != nil {
				return err
			}
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = "data/" + filepath.ToSlash(rel)
			if info.IsDir() {
				header.Name += "/"
				_, err = zw.CreateHeader(header)
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > remaining {
				return errBackupTooLarge
			}
			remaining -= info.Size()
			header.Method = zip.Deflate
			switch strings.ToLower(filepath.Ext(path)) {
			case ".3mf", ".png", ".jpg", ".jpeg", ".webp", ".gif", ".avif", ".zip", ".gz":
				header.Method = zip.Store
			}
			entry, err := zw.CreateHeader(header)
			if err != nil {
				return err
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(&backupWriter{ctx: ctx, writer: entry, remaining: info.Size()}, file)
			if copyErr == nil && n != info.Size() {
				copyErr = errors.New("backup source changed size")
			}
			return errors.Join(copyErr, file.Close())
		})
	}
	err = errors.Join(err, zw.Close())
	if err == nil {
		err = archive.Sync()
	}
	err = errors.Join(err, archive.Close())
	if err != nil {
		return "", err
	}
	keep = true
	return path, nil
}

func walkBackupData(ctx context.Context, root string, visit func(string, string, fs.FileInfo) error) error {
	excluded := map[string]bool{"fileament.db": true, "fileament.db-journal": true, "fileament.db-shm": true, "fileament.db-wal": true, "tmp": true, "backups": true, ".restore": true, ".mutations": true}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if excluded[rel] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("backup contains a symbolic link or unsupported file type")
		}
		return visit(path, rel, info)
	})
}

type backupWriter struct {
	ctx       context.Context
	writer    io.Writer
	remaining int64
}

func (w *backupWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.remaining {
		return 0, errBackupTooLarge
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
