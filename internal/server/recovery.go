package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
)

func (a *App) rebuildFromSidecars() error {
	modelsRoot := filepath.Join(a.cfg.DataDir, "models")
	return filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == modelsRoot {
			return err
		}
		sidecar := filepath.Join(path, "model.json")
		b, err := os.ReadFile(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.SkipDir
		}
		if err != nil {
			return err
		}
		var m Model
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if m.ID == "" || filepath.Base(path) != m.ID {
			return filepath.SkipDir
		}
		if err := a.upsertSidecarModel(m); err != nil {
			return err
		}
		return filepath.SkipDir
	})
}

func (a *App) upsertSidecarModel(m Model) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO models(id,title,description,source_url,license,author,primary_thumb,total_bytes,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET title=excluded.title, description=excluded.description, source_url=excluded.source_url, license=excluded.license, author=excluded.author, primary_thumb=excluded.primary_thumb, total_bytes=excluded.total_bytes, updated_at=excluded.updated_at`,
		m.ID, m.Title, m.Description, emptyNull(m.SourceURL), emptyNull(m.License), emptyNull(m.Author), emptyNull(m.PrimaryThumb), m.TotalBytes, m.CreatedAt, m.UpdatedAt); err != nil {
		return err
	}
	for _, f := range m.Files {
		if _, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sha256,triangle_count,bbox_x,bbox_y,bbox_z,thumb_path,sort_order)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET filename=excluded.filename, rel_path=excluded.rel_path, format=excluded.format, size_bytes=excluded.size_bytes, sha256=excluded.sha256, triangle_count=excluded.triangle_count, bbox_x=excluded.bbox_x, bbox_y=excluded.bbox_y, bbox_z=excluded.bbox_z, thumb_path=excluded.thumb_path, sort_order=excluded.sort_order`,
			f.ID, m.ID, f.Filename, f.RelPath, f.Format, f.SizeBytes, f.SHA256, f.TriangleCount, f.BBoxX, f.BBoxY, f.BBoxZ, emptyNull(f.ThumbPath), f.SortOrder); err != nil {
			return err
		}
		var n int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE file_id = ? AND type = 'thumbnail'`, f.ID).Scan(&n)
		if n == 0 && f.ThumbPath == "" {
			if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,?,?,?,?)`, ids.New(), "thumbnail", f.ID, "pending", time.Now().Unix()); err != nil {
				return err
			}
		}
	}
	for _, img := range m.Images {
		if _, err := tx.Exec(`INSERT INTO images(id,model_id,rel_path,sort_order) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET rel_path=excluded.rel_path, sort_order=excluded.sort_order`, img.ID, m.ID, img.RelPath, img.SortOrder); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM model_tags WHERE model_id = ?`, m.ID); err != nil {
		return err
	}
	for _, tag := range m.Tags {
		slug := slugify(tag)
		if slug == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO tags(id,name,slug) VALUES(?,?,?) ON CONFLICT(slug) DO UPDATE SET name=excluded.name`, "tag_"+slug, tag, slug); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO model_tags(model_id, tag_id) VALUES(?, (SELECT id FROM tags WHERE slug = ?))`, m.ID, slug); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) recoverThumbnailJobs() error {
	_, err := a.db.Exec(`UPDATE jobs SET status = 'pending', error = COALESCE(error, 'recovered after interrupted worker') WHERE type = 'thumbnail' AND status = 'running'`)
	return err
}
