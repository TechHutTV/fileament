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

	"github.com/TechHutTV/fileament/internal/ids"
	"github.com/TechHutTV/fileament/internal/mesh"
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
		r.Post("/api/models/{id}/files", a.handleAddModelFiles)
		r.Delete("/api/models/{id}/files/{fid}", a.handleDeleteModelFile)
		r.Post("/api/models/{id}/images", a.handleAddModelImages)
		r.Delete("/api/models/{id}/images/{imageID}", a.handleDeleteModelImage)
		r.Get("/api/tags", a.handleTags)
		r.Get("/files/{modelID}/{fileID}", a.handleDownload)
		r.Get("/mesh/{modelID}/{fileID}", a.handleMesh)
		r.Get("/images/{modelID}/{imageID}", a.handleOwnerImage)
		r.Put("/api/models/{id}/thumb", a.handleSetThumb)
	})
}

func (a *App) handleListModels(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 24)
	cursor := r.URL.Query().Get("cursor")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	collection := strings.TrimSpace(r.URL.Query().Get("collection"))
	sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortKey == "" {
		sortKey = "created"
	}
	args := []any{}
	where := []string{"1=1"}
	join := ""
	if q != "" {
		query := ftsQuery(q)
		if query == "" {
			where = append(where, "1=0")
		} else {
			join += " JOIN models_fts ON models_fts.rowid = models.rowid"
			where = append(where, "models_fts MATCH ?")
			args = append(args, query)
		}
	}
	if tag != "" {
		join += " JOIN model_tags mt ON mt.model_id = models.id JOIN tags tg ON tg.id = mt.tag_id"
		where = append(where, "tg.slug = ?")
		args = append(args, tag)
	}
	if collection != "" {
		join += " JOIN collection_models cm ON cm.model_id = models.id JOIN collections c ON c.id = cm.collection_id"
		where = append(where, "(c.id = ? OR c.slug = ?)")
		args = append(args, collection, collection)
	}
	order := "models.created_at DESC, models.id DESC"
	switch sortKey {
	case "updated":
		order = "models.updated_at DESC, models.id DESC"
	case "title":
		order = "models.title COLLATE NOCASE ASC, models.id ASC"
	case "size":
		order = "models.total_bytes DESC, models.id DESC"
	case "created":
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported sort"))
		return
	}
	if cursor != "" {
		decoded, err := decodeCursor(cursor)
		if err != nil || decoded.Sort != sortKey {
			writeError(w, http.StatusBadRequest, errors.New("invalid cursor"))
			return
		}
		switch sortKey {
		case "created":
			value, err := strconv.ParseInt(decoded.Value, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid cursor"))
				return
			}
			where = append(where, "(models.created_at < ? OR (models.created_at = ? AND models.id < ?))")
			args = append(args, value, value, decoded.ID)
		case "updated":
			value, err := strconv.ParseInt(decoded.Value, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid cursor"))
				return
			}
			where = append(where, "(models.updated_at < ? OR (models.updated_at = ? AND models.id < ?))")
			args = append(args, value, value, decoded.ID)
		case "size":
			value, err := strconv.ParseInt(decoded.Value, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid cursor"))
				return
			}
			where = append(where, "(models.total_bytes < ? OR (models.total_bytes = ? AND models.id < ?))")
			args = append(args, value, value, decoded.ID)
		case "title":
			where = append(where, "(models.title COLLATE NOCASE > ? OR (models.title COLLATE NOCASE = ? AND models.id > ?))")
			args = append(args, decoded.Value, decoded.Value, decoded.ID)
		}
	}
	args = append(args, limit+1)
	rows, err := a.db.Query(`SELECT DISTINCT models.id FROM models`+join+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+` LIMIT ?`, args...)
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
		next = encodeCursor(sortKey, last)
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
	upload, cleanup, err := a.streamSingleUpload(w, r)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	model, err := a.ingestStagedUpload(upload)
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, model)
}

type stagedUpload struct {
	Path     string
	Name     string
	StageDir string
}

func (a *App) streamSingleUpload(w http.ResponseWriter, r *http.Request) (stagedUpload, func(), error) {
	max := a.cfg.MaxUploadMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		return stagedUpload{}, nil, errors.Join(errBadUpload, err)
	}
	stage := filepath.Join(a.cfg.DataDir, "tmp", ids.New())
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return stagedUpload{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	var upload stagedUpload
	seenFile := false
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			return stagedUpload{}, nil, errors.Join(errBadUpload, err)
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			cleanup()
			return stagedUpload{}, nil, errors.Join(errBadUpload, errors.New("unexpected multipart part"))
		}
		if seenFile {
			_ = part.Close()
			cleanup()
			return stagedUpload{}, nil, errors.Join(errBadUpload, errors.New("only one file part is allowed"))
		}
		seenFile = true
		name := filepath.Base(part.FileName())
		dst, err := containedName(stage, name)
		if err != nil {
			_ = part.Close()
			cleanup()
			return stagedUpload{}, nil, errors.Join(errBadUpload, err)
		}
		if err := copyCapped(dst, part, max); err != nil {
			_ = part.Close()
			cleanup()
			return stagedUpload{}, nil, errors.Join(errBadUpload, err)
		}
		_ = part.Close()
		upload = stagedUpload{Path: dst, Name: name, StageDir: stage}
	}
	if !seenFile {
		cleanup()
		return stagedUpload{}, nil, errors.Join(errBadUpload, errors.New("file is required"))
	}
	return upload, cleanup, nil
}

func uploadStatus(err error) int {
	if errors.Is(err, errBadUpload) || errors.Is(err, errInvalidPath) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
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
	previous := m
	previous.Tags = append([]string(nil), m.Tags...)
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
	m, err = a.getModel(id)
	if err != nil {
		_ = a.updateModel(previous)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeSidecar(m); err != nil {
		_ = a.updateModel(previous)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *App) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.getModel(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if _, err := a.db.Exec(`DELETE FROM models WHERE id = ?`, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	root, err := containedName(filepath.Join(a.cfg.DataDir, "models"), id)
	if err == nil {
		_ = os.RemoveAll(root)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAddModelFiles(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")
	upload, cleanup, err := a.streamSingleUpload(w, r)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	m, err := a.appendStagedUpload(modelID, upload, true, true)
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (a *App) handleAddModelImages(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "id")
	upload, cleanup, err := a.streamSingleUpload(w, r)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	m, err := a.appendStagedUpload(modelID, upload, false, true)
	if err != nil {
		writeError(w, uploadStatus(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (a *App) handleDeleteModelFile(w http.ResponseWriter, r *http.Request) {
	modelID, fileID := chi.URLParam(r, "id"), chi.URLParam(r, "fid")
	var rel, thumb sql.NullString
	var size int64
	if err := a.db.QueryRow(`SELECT rel_path,size_bytes,thumb_path FROM files WHERE id = ? AND model_id = ?`, fileID, modelID).Scan(&rel, &size, &thumb); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM files WHERE id = ? AND model_id = ?`, fileID, modelID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(`DELETE FROM jobs WHERE file_id = ?`, fileID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(`UPDATE models SET total_bytes = MAX(total_bytes - ?, 0), primary_thumb = CASE WHEN primary_thumb = ? THEN '' ELSE primary_thumb END, updated_at = ? WHERE id = ?`, size, filepath.Base(thumb.String), time.Now().Unix(), modelID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	root := filepath.Join(a.cfg.DataDir, "models", modelID)
	if rel.Valid {
		if path, err := containedPath(root, rel.String); err == nil {
			_ = os.Remove(path)
		}
	}
	if thumb.Valid && thumb.String != "" {
		if path, err := containedPath(root, thumb.String); err == nil {
			_ = os.Remove(path)
		}
	}
	m, err := a.getModel(modelID)
	if err == nil {
		err = a.writeSidecar(m)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDeleteModelImage(w http.ResponseWriter, r *http.Request) {
	modelID, imageID := chi.URLParam(r, "id"), chi.URLParam(r, "imageID")
	var rel string
	var size int64
	root := filepath.Join(a.cfg.DataDir, "models", modelID)
	if err := a.db.QueryRow(`SELECT rel_path FROM images WHERE id = ? AND model_id = ?`, imageID, modelID).Scan(&rel); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if path, err := containedPath(root, rel); err == nil {
		if st, statErr := os.Stat(path); statErr == nil {
			size = st.Size()
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM images WHERE id = ? AND model_id = ?`, imageID, modelID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(`UPDATE models SET total_bytes = MAX(total_bytes - ?, 0), updated_at = ? WHERE id = ?`, size, time.Now().Unix(), modelID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if path, err := containedPath(root, rel); err == nil {
		_ = os.Remove(path)
	}
	m, err := a.getModel(modelID)
	if err == nil {
		err = a.writeSidecar(m)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
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
	path, err := containedPath(filepath.Join(a.cfg.DataDir, "models", modelID), rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleOwnerImage(w http.ResponseWriter, r *http.Request) {
	modelID, imageID := chi.URLParam(r, "modelID"), chi.URLParam(r, "imageID")
	var rel string
	err := a.db.QueryRow(`SELECT rel_path FROM images WHERE id = ? AND model_id = ?`, imageID, modelID).Scan(&rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, err := containedPath(filepath.Join(a.cfg.DataDir, "models", modelID), rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
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
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	var thumb sql.NullString
	if err := a.db.QueryRow(`SELECT thumb_path FROM files WHERE id = ? AND model_id = ?`, req.FileID, id).Scan(&thumb); err != nil || !thumb.Valid {
		writeError(w, http.StatusBadRequest, errors.New("thumbnail is not available"))
		return
	}
	thumbName := filepath.Base(thumb.String)
	if thumb.String != filepath.ToSlash(filepath.Join("thumbs", thumbName)) {
		writeError(w, http.StatusBadRequest, errors.New("thumbnail path is not supported"))
		return
	}
	ext := strings.ToLower(filepath.Ext(thumbName))
	if ext != ".jpg" && ext != ".png" {
		writeError(w, http.StatusBadRequest, errors.New("thumbnail format is not supported"))
		return
	}
	thumbDir := filepath.Join(a.cfg.DataDir, "models", id, "thumbs")
	sourcePath, err := containedName(thumbDir, thumbName)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("thumbnail path is not supported"))
		return
	}
	cardName := "card" + ext
	if err := copyFile(filepath.Join(thumbDir, cardName), sourcePath); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := a.db.Exec(`UPDATE models SET primary_thumb = ?, updated_at = ? WHERE id = ?`, cardName, time.Now().Unix(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	m, err := a.getModel(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeSidecar(m); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *App) ingestUpload(fh *multipart.FileHeader) (Model, error) {
	src, err := fh.Open()
	if err != nil {
		return Model{}, err
	}
	defer src.Close()
	stage := filepath.Join(a.cfg.DataDir, "tmp", ids.New())
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return Model{}, err
	}
	defer os.RemoveAll(stage)
	uploadPath, err := containedName(stage, filepath.Base(fh.Filename))
	if err != nil {
		return Model{}, errors.Join(errBadUpload, err)
	}
	if err := copyCapped(uploadPath, src, a.cfg.MaxUploadMB<<20); err != nil {
		return Model{}, errors.Join(errBadUpload, err)
	}
	return a.ingestStagedUpload(stagedUpload{Path: uploadPath, Name: filepath.Base(fh.Filename), StageDir: stage})
}

func (a *App) ingestStagedUpload(upload stagedUpload) (Model, error) {
	id := ids.New()
	now := time.Now().Unix()
	root := filepath.Join(a.cfg.DataDir, "models", id)
	stage := upload.StageDir
	for _, dir := range []string{filepath.Join(stage, "files"), filepath.Join(stage, "images"), filepath.Join(stage, "thumbs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Model{}, err
		}
	}
	defer os.RemoveAll(stage)
	title := strings.TrimSuffix(filepath.Base(upload.Name), filepath.Ext(upload.Name))
	description := ""
	var files []ModelFile
	var images []Image
	var err error
	if strings.EqualFold(filepath.Ext(upload.Name), ".zip") {
		files, images, description, err = a.extractBundle(stage, upload.Path, id)
		if err == nil {
			err = os.Remove(upload.Path)
		}
	} else {
		files, images, err = a.routeOneFile(stage, upload.Path, filepath.Base(upload.Name), id)
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
		_ = os.RemoveAll(root)
		return Model{}, err
	}
	if err := a.writeSidecar(model); err != nil {
		_, _ = a.db.Exec(`DELETE FROM models WHERE id = ?`, model.ID)
		_ = os.RemoveAll(root)
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

func (a *App) appendStagedUpload(modelID string, upload stagedUpload, allowMeshes, allowImages bool) (Model, error) {
	if _, err := a.getModel(modelID); err != nil {
		return Model{}, sql.ErrNoRows
	}
	for _, dir := range []string{filepath.Join(upload.StageDir, "files"), filepath.Join(upload.StageDir, "images"), filepath.Join(upload.StageDir, "thumbs")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Model{}, err
		}
	}
	var files []ModelFile
	var images []Image
	var err error
	if strings.EqualFold(filepath.Ext(upload.Name), ".zip") {
		files, images, _, err = a.extractBundle(upload.StageDir, upload.Path, modelID)
	} else {
		files, images, err = a.routeOneFile(upload.StageDir, upload.Path, filepath.Base(upload.Name), modelID)
	}
	if err != nil {
		return Model{}, errors.Join(errBadUpload, err)
	}
	if !allowMeshes && len(files) > 0 {
		return Model{}, errors.Join(errBadUpload, errors.New("mesh files are not accepted on this endpoint"))
	}
	if !allowImages && len(images) > 0 {
		return Model{}, errors.Join(errBadUpload, errors.New("images are not accepted on this endpoint"))
	}
	if len(files) == 0 && len(images) == 0 {
		return Model{}, errors.Join(errBadUpload, errors.New("upload contains no accepted files"))
	}
	var maxFileOrder, maxImageOrder int
	_ = a.db.QueryRow(`SELECT COALESCE(MAX(sort_order)+1,0) FROM files WHERE model_id = ?`, modelID).Scan(&maxFileOrder)
	_ = a.db.QueryRow(`SELECT COALESCE(MAX(sort_order)+1,0) FROM images WHERE model_id = ?`, modelID).Scan(&maxImageOrder)
	root := filepath.Join(a.cfg.DataDir, "models", modelID)
	var total int64
	for i := range files {
		files[i].SortOrder = maxFileOrder + i
		files[i].RelPath = filepath.ToSlash(filepath.Join("files", files[i].ID+"-"+files[i].Filename))
		dst, err := containedPath(root, files[i].RelPath)
		if err != nil {
			return Model{}, err
		}
		if err := os.Rename(filepath.Join(upload.StageDir, "files", files[i].Filename), dst); err != nil {
			return Model{}, err
		}
		total += files[i].SizeBytes
	}
	for i := range images {
		images[i].SortOrder = maxImageOrder + i
		old := images[i].RelPath
		images[i].RelPath = filepath.ToSlash(filepath.Join("images", images[i].ID+"-"+filepath.Base(old)))
		dst, err := containedPath(root, images[i].RelPath)
		if err != nil {
			return Model{}, err
		}
		if err := os.Rename(filepath.Join(upload.StageDir, old), dst); err != nil {
			return Model{}, err
		}
		if st, err := os.Stat(dst); err == nil {
			total += st.Size()
		}
	}
	tx, err := a.db.Begin()
	if err != nil {
		return Model{}, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, f := range files {
		if _, err := tx.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes,sha256,triangle_count,bbox_x,bbox_y,bbox_z,sort_order) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.ID, modelID, f.Filename, f.RelPath, f.Format, f.SizeBytes, f.SHA256, f.TriangleCount, f.BBoxX, f.BBoxY, f.BBoxZ, f.SortOrder); err != nil {
			return Model{}, err
		}
		if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?,?,?,?,?)`, ids.New(), "thumbnail", f.ID, "pending", now); err != nil {
			return Model{}, err
		}
	}
	for _, img := range images {
		if _, err := tx.Exec(`INSERT INTO images(id,model_id,rel_path,sort_order) VALUES(?,?,?,?)`, img.ID, modelID, img.RelPath, img.SortOrder); err != nil {
			return Model{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE models SET total_bytes = total_bytes + ?, updated_at = ? WHERE id = ?`, total, now, modelID); err != nil {
		return Model{}, err
	}
	if err := tx.Commit(); err != nil {
		return Model{}, err
	}
	m, err := a.getModel(modelID)
	if err != nil {
		return Model{}, err
	}
	if err := a.writeSidecar(m); err != nil {
		return Model{}, err
	}
	return m, nil
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

type listCursor struct {
	Sort  string `json:"sort"`
	Value string `json:"value"`
	ID    string `json:"id"`
}

func encodeCursor(sortKey string, model Model) string {
	value := model.Title
	switch sortKey {
	case "created":
		value = strconv.FormatInt(model.CreatedAt, 10)
	case "updated":
		value = strconv.FormatInt(model.UpdatedAt, 10)
	case "size":
		value = strconv.FormatInt(model.TotalBytes, 10)
	}
	b, _ := json.Marshal(listCursor{Sort: sortKey, Value: value, ID: model.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(raw string) (listCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return listCursor{}, err
	}
	var cursor listCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return listCursor{}, err
	}
	if cursor.Sort == "" || cursor.ID == "" {
		return listCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func ftsQuery(q string) string {
	parts := strings.Fields(q)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(p, `"'*`)
		p = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
				return r
			}
			return ' '
		}, p)
		for _, token := range strings.Fields(p) {
			out = append(out, `"`+strings.ReplaceAll(token, `"`, `""`)+`"*`)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " OR ")
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
