package server

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/brandon/fileament/internal/ids"
	"github.com/brandon/fileament/internal/mesh"
	"github.com/go-chi/chi/v5"
)

type Model struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Description  string      `json:"description"`
	SourceURL    string      `json:"sourceUrl,omitempty"`
	License      string      `json:"license,omitempty"`
	Author       string      `json:"author,omitempty"`
	PrimaryThumb string      `json:"primaryThumb,omitempty"`
	TotalBytes   int64       `json:"totalBytes"`
	CreatedAt    int64       `json:"createdAt"`
	UpdatedAt    int64       `json:"updatedAt"`
	Files        []ModelFile `json:"files,omitempty"`
	Images       []Image     `json:"images,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
}

type ModelFile struct {
	ID            string  `json:"id"`
	ModelID       string  `json:"modelId"`
	Filename      string  `json:"filename"`
	RelPath       string  `json:"relPath"`
	Format        string  `json:"format"`
	SizeBytes     int64   `json:"sizeBytes"`
	SHA256        string  `json:"sha256"`
	TriangleCount int     `json:"triangleCount"`
	BBoxX         float64 `json:"bboxX"`
	BBoxY         float64 `json:"bboxY"`
	BBoxZ         float64 `json:"bboxZ"`
	ThumbPath     string  `json:"thumbPath,omitempty"`
	SortOrder     int     `json:"sortOrder"`
}

type Image struct {
	ID        string `json:"id"`
	ModelID   string `json:"modelId"`
	RelPath   string `json:"relPath"`
	SortOrder int    `json:"sortOrder"`
}

var errBadUpload = errors.New("invalid upload")

func (a *App) mountModelRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(a.requireAuth)
		r.Get("/api/models", a.handleListModels)
		r.Post("/api/models", a.handleCreateModel)
		r.Get("/api/models/{id}", a.handleGetModel)
		r.Patch("/api/models/{id}", a.handlePatchModel)
		r.Delete("/api/models/{id}", a.handleDeleteModel)
		r.Get("/api/tags", a.handleTags)
		r.Get("/files/{modelID}/{fileID}", a.handleDownload)
		r.Get("/mesh/{modelID}/{fileID}", a.handleMesh)
		r.Put("/api/models/{id}/thumb", a.handleSetThumb)
	})
}

func (a *App) handleListModels(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 24)
	cursor := r.URL.Query().Get("cursor")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	args := []any{}
	where := []string{"1=1"}
	join := ""
	if q != "" {
		join += " JOIN models_fts fts ON fts.rowid = models.rowid"
		where = append(where, "models_fts MATCH ?")
		args = append(args, ftsQuery(q))
	}
	if tag != "" {
		join += " JOIN model_tags mt ON mt.model_id = models.id JOIN tags tg ON tg.id = mt.tag_id"
		where = append(where, "tg.slug = ?")
		args = append(args, tag)
	}
	if cursor != "" {
		created, id := decodeCursor(cursor)
		if created > 0 {
			where = append(where, "(models.created_at < ? OR (models.created_at = ? AND models.id < ?))")
			args = append(args, created, created, id)
		}
	}
	args = append(args, limit+1)
	rows, err := a.db.Query(`SELECT DISTINCT models.id FROM models`+join+` WHERE `+strings.Join(where, " AND ")+` ORDER BY models.created_at DESC, models.id DESC LIMIT ?`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		ids = append(ids, id)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		last, _ := a.getModel(ids[len(ids)-1])
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	items := make([]Model, 0, len(ids))
	for _, id := range ids {
		m, err := a.getModel(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (a *App) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	fhs := r.MultipartForm.File["file"]
	if len(fhs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("file is required"))
		return
	}
	model, err := a.ingestUpload(fhs[0])
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errBadUpload) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusCreated, model)
}

func (a *App) handleGetModel(w http.ResponseWriter, r *http.Request) {
	m, err := a.getModel(chi.URLParam(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

type patchModelRequest struct {
	Title       *string  `json:"title"`
	Description *string  `json:"description"`
	SourceURL   *string  `json:"sourceUrl"`
	License     *string  `json:"license"`
	Author      *string  `json:"author"`
	Tags        []string `json:"tags"`
}

func (a *App) handlePatchModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := a.getModel(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req patchModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Title != nil {
		m.Title = strings.TrimSpace(*req.Title)
	}
	if req.Description != nil {
		m.Description = *req.Description
	}
	if req.SourceURL != nil {
		m.SourceURL = *req.SourceURL
	}
	if req.License != nil {
		m.License = *req.License
	}
	if req.Author != nil {
		m.Author = *req.Author
	}
	if req.Tags != nil {
		m.Tags = normalizeTags(req.Tags)
	}
	m.UpdatedAt = time.Now().Unix()
	if err := a.updateModel(m); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	m, _ = a.getModel(id)
	_ = a.writeSidecar(m)
	writeJSON(w, http.StatusOK, m)
}

func (a *App) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.db.Exec(`DELETE FROM models WHERE id = ?`, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = os.RemoveAll(filepath.Join(a.cfg.DataDir, "models", id))
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleTags(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT name, slug FROM tags ORDER BY name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	var tags []map[string]string
	for rows.Next() {
		var name, slug string
		_ = rows.Scan(&name, &slug)
		tags = append(tags, map[string]string{"name": name, "slug": slug})
	}
	writeJSON(w, http.StatusOK, tags)
}

func (a *App) handleDownload(w http.ResponseWriter, r *http.Request) {
	a.serveModelFile(w, r, true)
}

func (a *App) handleMesh(w http.ResponseWriter, r *http.Request) {
	a.serveModelFile(w, r, false)
}

func (a *App) serveModelFile(w http.ResponseWriter, r *http.Request, attachment bool) {
	modelID, fileID := chi.URLParam(r, "modelID"), chi.URLParam(r, "fileID")
	var filename, rel string
	err := a.db.QueryRow(`SELECT filename, rel_path FROM files WHERE id = ? AND model_id = ?`, fileID, modelID).Scan(&filename, &rel)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if attachment {
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(filename, `"`, "")+`"`)
	}
	http.ServeFile(w, r, filepath.Join(a.cfg.DataDir, "models", modelID, rel))
}

func (a *App) handleSetThumb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		FileID string `json:"fileId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var thumb sql.NullString
	if err := a.db.QueryRow(`SELECT thumb_path FROM files WHERE id = ? AND model_id = ?`, req.FileID, id).Scan(&thumb); err != nil || !thumb.Valid {
		writeError(w, http.StatusBadRequest, errors.New("thumbnail is not available"))
		return
	}
	if err := copyFile(filepath.Join(a.cfg.DataDir, "models", id, "thumbs", "card.jpg"), filepath.Join(a.cfg.DataDir, "models", id, thumb.String)); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := a.db.Exec(`UPDATE models SET primary_thumb = 'card.jpg', updated_at = ? WHERE id = ?`, time.Now().Unix(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	m, _ := a.getModel(id)
	_ = a.writeSidecar(m)
	writeJSON(w, http.StatusOK, m)
}

func (a *App) ingestUpload(fh *multipart.FileHeader) (Model, error) {
	id := ids.New()
	now := time.Now().Unix()
	root := filepath.Join(a.cfg.DataDir, "models", id)
	stage := filepath.Join(a.cfg.DataDir, "tmp", id)
	for _, dir := range []string{stage, filepath.Join(stage, "files"), filepath.Join(stage, "images"), filepath.Join(stage, "thumbs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Model{}, err
		}
	}
	defer os.RemoveAll(stage)
	src, err := fh.Open()
	if err != nil {
		return Model{}, err
	}
	defer src.Close()
	uploadPath := filepath.Join(stage, filepath.Base(fh.Filename))
	if err := copyCapped(uploadPath, src, a.cfg.MaxUploadMB<<20); err != nil {
		return Model{}, errors.Join(errBadUpload, err)
	}
	title := strings.TrimSuffix(filepath.Base(fh.Filename), filepath.Ext(fh.Filename))
	description := ""
	var files []ModelFile
	var images []Image
	if strings.EqualFold(filepath.Ext(fh.Filename), ".zip") {
		files, images, description, err = a.extractBundle(stage, uploadPath, id)
	} else {
		files, images, err = a.routeOneFile(stage, uploadPath, filepath.Base(fh.Filename), id)
	}
	if err != nil {
		return Model{}, errors.Join(errBadUpload, err)
	}
	if len(files) == 0 {
		return Model{}, errors.Join(errBadUpload, errors.New("bundle contains no supported mesh files"))
	}
	if strings.EqualFold(title, "bundle") && len(files) > 0 {
		title = strings.TrimSuffix(files[0].Filename, filepath.Ext(files[0].Filename))
	}
	var total int64
	for _, f := range files {
		total += f.SizeBytes
	}
	for _, img := range images {
		if st, err := os.Stat(filepath.Join(stage, img.RelPath)); err == nil {
			total += st.Size()
		}
	}
	model := Model{ID: id, Title: title, Description: description, TotalBytes: total, CreatedAt: now, UpdatedAt: now, Files: files, Images: images}
	if err := os.Rename(stage, root); err != nil {
		return Model{}, err
	}
	if err := a.insertModel(model); err != nil {
		return Model{}, err
	}
	if err := a.writeSidecar(model); err != nil {
		return Model{}, err
	}
	return model, nil
}

func (a *App) extractBundle(stage, uploadPath, modelID string) ([]ModelFile, []Image, string, error) {
	zr, err := zip.OpenReader(uploadPath)
	if err != nil {
		return nil, nil, "", err
	}
	defer zr.Close()
	var files []ModelFile
	var images []Image
	var description string
	var total int64
	for _, zf := range zr.File {
		name := filepath.ToSlash(zf.Name)
		if shouldDiscard(name) || zf.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return nil, nil, "", errors.New("zip entry escapes destination")
		}
		total += int64(zf.UncompressedSize64)
		if total > a.cfg.MaxUploadMB<<20 {
			return nil, nil, "", errors.New("zip exceeds uncompressed size cap")
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, nil, "", err
		}
		switch classifyExt(clean) {
		case "mesh":
			dstName := uniqueName(filepath.Base(clean), len(files))
			dst := filepath.Join(stage, "files", dstName)
			if err := copyCapped(dst, rc, int64(zf.UncompressedSize64)); err != nil {
				_ = rc.Close()
				return nil, nil, "", err
			}
			_ = rc.Close()
			mf, err := a.buildFileRecord(modelID, dst, filepath.ToSlash(filepath.Join("files", dstName)), len(files))
			if err != nil {
				return nil, nil, "", err
			}
			files = append(files, mf)
		case "image":
			dstName := uniqueName(filepath.Base(clean), len(images))
			dst := filepath.Join(stage, "images", dstName)
			if err := copyCapped(dst, rc, int64(zf.UncompressedSize64)); err != nil {
				_ = rc.Close()
				return nil, nil, "", err
			}
			_ = rc.Close()
			images = append(images, Image{ID: ids.New(), ModelID: modelID, RelPath: filepath.ToSlash(filepath.Join("images", dstName)), SortOrder: len(images)})
		case "readme":
			b, _ := io.ReadAll(io.LimitReader(rc, 256*1024))
			_ = rc.Close()
			if description == "" {
				description = strings.TrimSpace(string(b))
			}
		default:
			_ = rc.Close()
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
	for i := range files {
		files[i].SortOrder = i
	}
	return files, images, description, nil
}

func (a *App) routeOneFile(stage, srcPath, name, modelID string) ([]ModelFile, []Image, error) {
	switch classifyExt(name) {
	case "mesh":
		dst := filepath.Join(stage, "files", filepath.Base(name))
		if err := os.Rename(srcPath, dst); err != nil {
			return nil, nil, err
		}
		mf, err := a.buildFileRecord(modelID, dst, filepath.ToSlash(filepath.Join("files", filepath.Base(name))), 0)
		if err != nil {
			return nil, nil, err
		}
		return []ModelFile{mf}, nil, nil
	case "image":
		dst := filepath.Join(stage, "images", filepath.Base(name))
		if err := os.Rename(srcPath, dst); err != nil {
			return nil, nil, err
		}
		return nil, []Image{{ID: ids.New(), ModelID: modelID, RelPath: filepath.ToSlash(filepath.Join("images", filepath.Base(name)))}}, nil
	default:
		return nil, nil, errors.New("unsupported upload")
	}
}

func (a *App) buildFileRecord(modelID, absPath, relPath string, order int) (ModelFile, error) {
	st, err := os.Stat(absPath)
	if err != nil {
		return ModelFile{}, err
	}
	sum, err := shaFile(absPath)
	if err != nil {
		return ModelFile{}, err
	}
	stats, _, err := mesh.ParseFile(absPath)
	if err != nil {
		return ModelFile{}, err
	}
	return ModelFile{
		ID: ids.New(), ModelID: modelID, Filename: filepath.Base(relPath), RelPath: relPath, Format: stats.Format,
		SizeBytes: st.Size(), SHA256: sum, TriangleCount: stats.TriangleCount, BBoxX: stats.BBoxX, BBoxY: stats.BBoxY, BBoxZ: stats.BBoxZ, SortOrder: order,
	}, nil
}

func (a *App) insertModel(model Model) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO models(id,title,description,total_bytes,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		model.ID, model.Title, model.Description, model.TotalBytes, model.CreatedAt, model.UpdatedAt); err != nil {
		return err
	}
	for _, f := range model.Files {
		if _, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sha256,triangle_count,bbox_x,bbox_y,bbox_z,sort_order) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.ID, model.ID, f.Filename, f.RelPath, f.Format, f.SizeBytes, f.SHA256, f.TriangleCount, f.BBoxX, f.BBoxY, f.BBoxZ, f.SortOrder); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,?,?,?,?)`, ids.New(), "thumbnail", f.ID, "pending", time.Now().Unix()); err != nil {
			return err
		}
	}
	for _, img := range model.Images {
		if _, err := tx.Exec(`INSERT INTO images(id,model_id,rel_path,sort_order) VALUES(?,?,?,?)`, img.ID, model.ID, img.RelPath, img.SortOrder); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) writeSidecar(model Model) error {
	path := filepath.Join(a.cfg.DataDir, "models", model.ID, "model.json")
	tmp := path + ".tmp"
	b, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) getModel(id string) (Model, error) {
	var m Model
	err := a.db.QueryRow(`SELECT id,title,description,COALESCE(source_url,''),COALESCE(license,''),COALESCE(author,''),COALESCE(primary_thumb,''),total_bytes,created_at,updated_at FROM models WHERE id = ?`, id).
		Scan(&m.ID, &m.Title, &m.Description, &m.SourceURL, &m.License, &m.Author, &m.PrimaryThumb, &m.TotalBytes, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return m, err
	}
	rows, err := a.db.Query(`SELECT id,filename,rel_path,format,size_bytes,sha256,COALESCE(triangle_count,0),COALESCE(bbox_x,0),COALESCE(bbox_y,0),COALESCE(bbox_z,0),COALESCE(thumb_path,''),sort_order FROM files WHERE model_id = ? ORDER BY sort_order, filename`, id)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var f ModelFile
		f.ModelID = id
		if err := rows.Scan(&f.ID, &f.Filename, &f.RelPath, &f.Format, &f.SizeBytes, &f.SHA256, &f.TriangleCount, &f.BBoxX, &f.BBoxY, &f.BBoxZ, &f.ThumbPath, &f.SortOrder); err != nil {
			return m, err
		}
		m.Files = append(m.Files, f)
	}
	imgRows, err := a.db.Query(`SELECT id,rel_path,sort_order FROM images WHERE model_id = ? ORDER BY sort_order`, id)
	if err != nil {
		return m, err
	}
	defer imgRows.Close()
	for imgRows.Next() {
		var img Image
		img.ModelID = id
		_ = imgRows.Scan(&img.ID, &img.RelPath, &img.SortOrder)
		m.Images = append(m.Images, img)
	}
	tagRows, err := a.db.Query(`SELECT tags.name FROM tags JOIN model_tags ON tags.id = model_tags.tag_id WHERE model_tags.model_id = ? ORDER BY tags.name`, id)
	if err != nil {
		return m, err
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var tag string
		_ = tagRows.Scan(&tag)
		m.Tags = append(m.Tags, tag)
	}
	return m, nil
}

func (a *App) updateModel(m Model) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE models SET title=?, description=?, source_url=?, license=?, author=?, updated_at=? WHERE id=?`, m.Title, m.Description, emptyNull(m.SourceURL), emptyNull(m.License), emptyNull(m.Author), m.UpdatedAt, m.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM model_tags WHERE model_id = ?`, m.ID); err != nil {
		return err
	}
	for _, tag := range m.Tags {
		slug := slugify(tag)
		tagID := "tag_" + slug
		if _, err := tx.Exec(`INSERT INTO tags(id,name,slug) VALUES(?,?,?) ON CONFLICT(slug) DO UPDATE SET name=excluded.name`, tagID, tag, slug); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO model_tags(model_id, tag_id) VALUES(?, (SELECT id FROM tags WHERE slug = ?))`, m.ID, slug); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func parseLimit(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 || n > 100 {
		return fallback
	}
	return n
}

func encodeCursor(created int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(created, 10) + ":" + id))
}

func decodeCursor(raw string) (int64, string) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, ""
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return 0, ""
	}
	n, _ := strconv.ParseInt(parts[0], 10, 64)
	return n, parts[1]
}

func ftsQuery(q string) string {
	parts := strings.Fields(q)
	for i, p := range parts {
		parts[i] = strings.Trim(p, `"'*`) + "*"
	}
	return strings.Join(parts, " ")
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[slugify(tag)] {
			continue
		}
		seen[slugify(tag)] = true
		out = append(out, tag)
	}
	return out
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func emptyNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func copyCapped(dst string, src io.Reader, capBytes int64) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(src, capBytes+1))
	if err != nil {
		return err
	}
	if n > capBytes {
		return errors.New("upload exceeds size cap")
	}
	return nil
}

func shaFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func classifyExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".stl", ".3mf", ".obj":
		return "mesh"
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return "image"
	case ".txt", ".md":
		return "readme"
	default:
		if strings.EqualFold(filepath.Base(name), "README") {
			return "readme"
		}
		return ""
	}
}

func shouldDiscard(name string) bool {
	base := filepath.Base(name)
	return strings.HasPrefix(name, "__MACOSX/") || base == ".DS_Store" || strings.EqualFold(base, "Thumbs.db")
}

func uniqueName(name string, index int) string {
	if index == 0 {
		return filepath.Base(name)
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(filepath.Base(name), ext)
	return stem + "-" + strconv.FormatInt(int64(index), 10) + ext
}
