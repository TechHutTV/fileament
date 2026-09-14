package server

import (
	"context"
	"database/sql"
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
	wanted := map[string]bool{}
	err := filepath.WalkDir(modelsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == modelsRoot {
			return err
		}
		sidecar := filepath.Join(path, "model.json")
		b, err := os.ReadFile(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("model directory is missing its durable sidecar; recovery is required")
		}
		if err != nil {
			return err
		}
		var m Model
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if m.ID == "" || filepath.Base(path) != m.ID {
			return errors.New("model sidecar identifier does not match its directory")
		}
		if err := a.upsertSidecarModel(m); err != nil {
			return err
		}
		wanted[m.ID] = true
		return filepath.SkipDir
	})
	if err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := pruneIndexedRows(tx, "models", "", "", wanted); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE NOT EXISTS (SELECT 1 FROM model_tags WHERE tag_id = tags.id)`); err != nil {
		return err
	}
	return tx.Commit()
}

// rebuildCollectionsFromSidecar is the startup path: a missing sidecar next to indexed
// collections means durable state was lost, so startup stops instead of discarding the index.
func (a *App) rebuildCollectionsFromSidecar() error {
	return a.syncCollectionsIndexToSidecar(false)
}

// syncCollectionsIndexToSidecar makes the collections index match the durable sidecar. With
// missingMeansEmpty set, an absent sidecar is authoritative and indexed collections are removed;
// mutation rollback relies on that after restoring a pre-mutation state that had no sidecar.
func (a *App) syncCollectionsIndexToSidecar(missingMeansEmpty bool) error {
	path := filepath.Join(a.cfg.DataDir, "collections.json")
	b, err := os.ReadFile(path)
	missing := errors.Is(err, os.ErrNotExist)
	if missing {
		b, err = []byte("null"), nil
	}
	if err != nil {
		return err
	}
	var collections []Collection
	if err := json.Unmarshal(b, &collections); err != nil {
		return err
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if missing && !missingMeansEmpty {
		var indexed int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&indexed); err != nil {
			return err
		}
		if indexed != 0 {
			return errors.New("collections index has entries but its durable sidecar is missing; recovery is required")
		}
	}
	wanted := map[string]bool{}
	for _, c := range collections {
		if c.ID != "" && c.Name != "" && c.Slug != "" {
			wanted[c.ID] = true
		}
	}
	if err := pruneIndexedRows(tx, "collections", "", "", wanted); err != nil {
		return err
	}
	for _, c := range collections {
		if c.ID == "" || c.Name == "" || c.Slug == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO collections(id,name,slug,description,cover_model_id,created_at)
VALUES(?,?,?,?,NULLIF(?,''),?)
ON CONFLICT(id) DO UPDATE SET name=excluded.name, slug=excluded.slug, description=excluded.description, cover_model_id=excluded.cover_model_id`,
			c.ID, c.Name, c.Slug, c.Description, c.CoverModelID, c.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM collection_models WHERE collection_id = ?`, c.ID); err != nil {
			return err
		}
		for order, modelID := range c.ModelIDs {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO collection_models(collection_id, model_id, sort_order) VALUES(?,?,?)`, c.ID, modelID, order); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (a *App) upsertSidecarModel(m Model) error {
	if err := validateSidecarModel(m); err != nil {
		return err
	}
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
	files, images := map[string]bool{}, map[string]bool{}
	for _, f := range m.Files {
		files[f.ID] = true
		result, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sha256,triangle_count,bbox_x,bbox_y,bbox_z,thumb_path,sort_order)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET filename=excluded.filename, rel_path=excluded.rel_path, format=excluded.format, size_bytes=excluded.size_bytes, sha256=excluded.sha256, triangle_count=excluded.triangle_count, bbox_x=excluded.bbox_x, bbox_y=excluded.bbox_y, bbox_z=excluded.bbox_z, thumb_path=excluded.thumb_path, sort_order=excluded.sort_order
WHERE files.model_id = excluded.model_id`,
			f.ID, m.ID, f.Filename, f.RelPath, f.Format, f.SizeBytes, f.SHA256, f.TriangleCount, f.BBoxX, f.BBoxY, f.BBoxZ, emptyNull(f.ThumbPath), f.SortOrder)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.New("file identifier belongs to another model")
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE file_id = ? AND type = 'thumbnail'`, f.ID).Scan(&n); err != nil {
			return err
		}
		if n == 0 && f.ThumbPath == "" {
			if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,?,?,?,?)`, ids.New(), "thumbnail", f.ID, "pending", time.Now().Unix()); err != nil {
				return err
			}
		}
	}
	for _, img := range m.Images {
		images[img.ID] = true
		result, err := tx.Exec(`INSERT INTO images(id,model_id,rel_path,sort_order) VALUES(?,?,?,?)
ON CONFLICT(id) DO UPDATE SET rel_path=excluded.rel_path, sort_order=excluded.sort_order
WHERE images.model_id = excluded.model_id`, img.ID, m.ID, img.RelPath, img.SortOrder)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.New("image identifier belongs to another model")
		}
	}
	if err := pruneIndexedRows(tx, "files", "model_id", m.ID, files); err != nil {
		return err
	}
	if err := pruneIndexedRows(tx, "images", "model_id", m.ID, images); err != nil {
		return err
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
	if _, err := a.db.Exec(`UPDATE jobs SET status = 'pending', finished_at = NULL, error = COALESCE(error, 'recovered after interrupted worker') WHERE type = 'thumbnail' AND status = 'running'`); err != nil {
		return err
	}
	return a.pruneThumbnailJobs(context.Background(), time.Now())
}

func pruneIndexedRows(tx *sql.Tx, table, parentColumn, parentID string, wanted map[string]bool) error {
	query := "SELECT id FROM " + table
	var args []any
	if parentColumn != "" {
		query += " WHERE " + parentColumn + " = ?"
		args = append(args, parentID)
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !wanted[id] {
			stale = append(stale, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE id = ?", id); err != nil {
			return err
		}
	}
	return nil
}
