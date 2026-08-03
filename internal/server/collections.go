package server

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
	"github.com/go-chi/chi/v5"
)

type Collection struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Description  string   `json:"description"`
	CoverModelID string   `json:"coverModelId,omitempty"`
	CreatedAt    int64    `json:"createdAt"`
	ModelIDs     []string `json:"modelIds,omitempty"`
	Models       []Model  `json:"models,omitempty"`
}

type collectionSummary struct {
	Collection
	CoverThumb string `json:"coverThumb,omitempty"`
}

type ShareLink struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Scope     string `json:"scope"`
	TargetID  string `json:"targetId"`
	Label     string `json:"label,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	RevokedAt int64  `json:"revokedAt,omitempty"`
	HitCount  int64  `json:"hitCount"`
	CreatedAt int64  `json:"createdAt"`
}

func (a *App) mountCollectionRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(a.requireAuth)
		r.Get("/api/collections", a.handleListCollections)
		r.Post("/api/collections", a.handleCreateCollection)
		r.Get("/api/collections/{id}", a.handleGetCollection)
		r.Patch("/api/collections/{id}", a.handlePatchCollection)
		r.Delete("/api/collections/{id}", a.handleDeleteCollection)
		r.Put("/api/collections/{id}/models/{mid}", a.handleAddCollectionModel)
		r.Delete("/api/collections/{id}/models/{mid}", a.handleRemoveCollectionModel)
		r.Put("/api/collections/{id}/order", a.handleReorderCollectionModels)
		r.Get("/api/shares", a.handleListShares)
		r.Post("/api/shares", a.handleCreateShare)
		r.Delete("/api/shares/{id}", a.handleRevokeShare)
	})
	r.Get("/api/public/{token}", a.handlePublic)
	r.Get("/api/public/{token}/files/{fid}", a.handlePublicFile)
	r.Get("/api/public/{token}/mesh/{fid}", a.handlePublicMesh)
	r.Get("/api/public/{token}/thumbs/{name}", a.handlePublicThumb)
	r.Get("/api/public/{token}/images/{imageID}", a.handlePublicImage)
}

func (a *App) handleListCollections(w http.ResponseWriter, r *http.Request) {
	collections, err := a.listCollections()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]collectionSummary, len(collections))
	indexes := make(map[string]int, len(collections))
	for i, collection := range collections {
		out[i].Collection = collection
		indexes[collection.ID] = i
	}
	rows, err := a.db.Query(`SELECT c.id, COALESCE(m.primary_thumb, '') FROM collections c LEFT JOIN models m ON m.id = c.cover_model_id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, coverThumb string
		if err := rows.Scan(&id, &coverThumb); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if i, ok := indexes[id]; ok {
			out[i].CoverThumb = coverThumb
		}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) listCollections() ([]Collection, error) {
	rows, err := a.db.Query(`SELECT id,name,slug,description,COALESCE(cover_model_id,''),created_at FROM collections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var out []Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.CoverModelID, &c.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	indexes := make(map[string]int, len(out))
	for i := range out {
		indexes[out[i].ID] = i
	}
	members, err := a.db.Query(`SELECT collection_id, model_id FROM collection_models ORDER BY collection_id, sort_order`)
	if err != nil {
		return nil, err
	}
	defer members.Close()
	for members.Next() {
		var collectionID, modelID string
		if err := members.Scan(&collectionID, &modelID); err != nil {
			return nil, err
		}
		if i, ok := indexes[collectionID]; ok {
			out[i].ModelIDs = append(out[i].ModelIDs, modelID)
		}
	}
	return out, members.Err()
}

func (a *App) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req Collection
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	now := time.Now().Unix()
	c := Collection{ID: ids.New(), Name: strings.TrimSpace(req.Name), Slug: slugify(req.Name), Description: req.Description, CoverModelID: req.CoverModelID, CreatedAt: now}
	if c.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if c.CoverModelID != "" {
		writeError(w, http.StatusBadRequest, errors.New("add a model before selecting a cover"))
		return
	}
	if _, err := a.db.Exec(`INSERT INTO collections(id,name,slug,description,cover_model_id,created_at) VALUES(?,?,?,?,NULLIF(?,''),?)`, c.ID, c.Name, c.Slug, c.Description, c.CoverModelID, c.CreatedAt); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		_, _ = a.db.Exec(`DELETE FROM collections WHERE id = ?`, c.ID)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *App) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	c, err := a.getCollection(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *App) handlePatchCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req Collection
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if req.CoverModelID != "" {
		if !a.collectionContains(id, req.CoverModelID) {
			writeError(w, http.StatusBadRequest, errors.New("cover model must belong to the collection"))
			return
		}
	}
	if _, err := a.db.Exec(`UPDATE collections SET name=?, slug=?, description=?, cover_model_id=NULLIF(?, '') WHERE id=?`, req.Name, slugify(req.Name), req.Description, req.CoverModelID, id); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	c, err := a.getCollection(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *App) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	res, err := a.db.Exec(`DELETE FROM collections WHERE id = ?`, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, sql.ErrNoRows)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleAddCollectionModel(w http.ResponseWriter, r *http.Request) {
	id, mid := chi.URLParam(r, "id"), chi.URLParam(r, "mid")
	if _, err := a.getCollection(id); err != nil {
		writeError(w, http.StatusNotFound, errors.New("collection not found"))
		return
	}
	if _, err := a.getModel(mid); err != nil {
		writeError(w, http.StatusNotFound, errors.New("model not found"))
		return
	}
	var n int
	_ = a.db.QueryRow(`SELECT COALESCE(MAX(sort_order)+1,0) FROM collection_models WHERE collection_id = ?`, id).Scan(&n)
	if _, err := a.db.Exec(`INSERT OR REPLACE INTO collection_models(collection_id, model_id, sort_order) VALUES(?,?,?)`, id, mid, n); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleRemoveCollectionModel(w http.ResponseWriter, r *http.Request) {
	id, modelID := chi.URLParam(r, "id"), chi.URLParam(r, "mid")
	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM collection_models WHERE collection_id = ? AND model_id = ?`, id, modelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, sql.ErrNoRows)
		return
	}
	if _, err := tx.Exec(`UPDATE collections SET cover_model_id = NULL WHERE id = ? AND cover_model_id = ?`, id, modelID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleReorderCollectionModels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	collection, err := a.getCollection(id)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("collection not found"))
		return
	}
	var req struct {
		ModelIDs []string `json:"modelIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.ModelIDs) != len(collection.ModelIDs) {
		writeError(w, http.StatusBadRequest, errors.New("modelIds must contain every collection model exactly once"))
		return
	}
	want := make(map[string]bool, len(collection.ModelIDs))
	for _, modelID := range collection.ModelIDs {
		want[modelID] = true
	}
	for _, modelID := range req.ModelIDs {
		if !want[modelID] {
			writeError(w, http.StatusBadRequest, errors.New("modelIds must contain every collection model exactly once"))
			return
		}
		delete(want, modelID)
	}
	if len(want) != 0 {
		writeError(w, http.StatusBadRequest, errors.New("modelIds must contain every collection model exactly once"))
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	for order, modelID := range req.ModelIDs {
		if _, err := tx.Exec(`UPDATE collection_models SET sort_order = ? WHERE collection_id = ? AND model_id = ?`, order, id, modelID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := a.writeCollectionsSidecar(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) getCollection(idOrSlug string) (Collection, error) {
	var c Collection
	err := a.db.QueryRow(`SELECT id,name,slug,description,COALESCE(cover_model_id,''),created_at FROM collections WHERE id = ? OR slug = ?`, idOrSlug, idOrSlug).Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.CoverModelID, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	rows, err := a.db.Query(`SELECT model_id FROM collection_models WHERE collection_id = ? ORDER BY sort_order`, c.ID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return c, err
		}
		c.ModelIDs = append(c.ModelIDs, id)
		m, err := a.getModel(id)
		if err != nil {
			return c, err
		}
		c.Models = append(c.Models, m)
	}
	return c, rows.Err()
}

func (a *App) writeCollectionsSidecar() error {
	collections, err := a.listCollections()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(collections, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(a.cfg.DataDir, "collections.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *App) handleListShares(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT id,token,scope,target_id,COALESCE(label,''),COALESCE(expires_at,0),COALESCE(revoked_at,0),hit_count,created_at FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	var out []ShareLink
	for rows.Next() {
		var s ShareLink
		if err := rows.Scan(&s.ID, &s.Token, &s.Scope, &s.TargetID, &s.Label, &s.ExpiresAt, &s.RevokedAt, &s.HitCount, &s.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *App) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req ShareLink
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Scope != "model" && req.Scope != "collection" {
		writeError(w, http.StatusBadRequest, errors.New("scope must be model or collection"))
		return
	}
	if req.TargetID == "" {
		writeError(w, http.StatusBadRequest, errors.New("targetId is required"))
		return
	}
	if req.ExpiresAt > 0 && req.ExpiresAt <= time.Now().Unix() {
		writeError(w, http.StatusBadRequest, errors.New("expiresAt must be in the future"))
		return
	}
	if req.Scope == "model" {
		if _, err := a.getModel(req.TargetID); err != nil {
			writeError(w, http.StatusNotFound, errors.New("model not found"))
			return
		}
	} else if _, err := a.getCollection(req.TargetID); err != nil {
		writeError(w, http.StatusNotFound, errors.New("collection not found"))
		return
	}
	token, err := randomBase62(22)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s := ShareLink{ID: ids.New(), Token: token, Scope: req.Scope, TargetID: req.TargetID, Label: req.Label, ExpiresAt: req.ExpiresAt, CreatedAt: time.Now().Unix()}
	if _, err := a.db.Exec(`INSERT INTO share_links(id,token,scope,target_id,label,expires_at,created_at) VALUES(?,?,?,?,?,NULLIF(?,0),?)`, s.ID, s.Token, s.Scope, s.TargetID, s.Label, s.ExpiresAt, s.CreatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (a *App) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	res, err := a.db.Exec(`UPDATE share_links SET revoked_at = ? WHERE id = ?`, time.Now().Unix(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, sql.ErrNoRows)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	share, err := a.resolveShare(chi.URLParam(r, "token"))
	if err != nil {
		publicError(w, err)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	if share.Scope == "model" {
		m, err := a.getModel(share.TargetID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"share": share, "model": m})
		return
	}
	c, err := a.getCollection(share.TargetID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"share": share, "collection": c})
}

func (a *App) handlePublicFile(w http.ResponseWriter, r *http.Request) {
	a.servePublicAsset(w, r, true)
}

func (a *App) handlePublicMesh(w http.ResponseWriter, r *http.Request) {
	a.servePublicAsset(w, r, false)
}

func (a *App) servePublicAsset(w http.ResponseWriter, r *http.Request, attachment bool) {
	share, err := a.resolveShare(chi.URLParam(r, "token"))
	if err != nil {
		publicError(w, err)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	fileID := chi.URLParam(r, "fid")
	modelID, ok := a.publicFileAllowed(share, fileID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var filename, rel string
	if err := a.db.QueryRow(`SELECT filename, rel_path FROM files WHERE id = ? AND model_id = ?`, fileID, modelID).Scan(&filename, &rel); err != nil {
		http.NotFound(w, r)
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

func (a *App) handlePublicThumb(w http.ResponseWriter, r *http.Request) {
	share, err := a.resolveShare(chi.URLParam(r, "token"))
	if err != nil {
		publicError(w, err)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	modelID := share.TargetID
	if share.Scope == "collection" {
		modelID = r.URL.Query().Get("model")
		if !a.collectionContains(share.TargetID, modelID) {
			http.NotFound(w, r)
			return
		}
	}
	if !a.thumbAllowed(modelID, chi.URLParam(r, "name")) {
		http.NotFound(w, r)
		return
	}
	path, err := containedName(filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs"), chi.URLParam(r, "name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handlePublicImage(w http.ResponseWriter, r *http.Request) {
	share, err := a.resolveShare(chi.URLParam(r, "token"))
	if err != nil {
		publicError(w, err)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	imageID := chi.URLParam(r, "imageID")
	var modelID, rel string
	if err := a.db.QueryRow(`SELECT model_id, rel_path FROM images WHERE id = ?`, imageID).Scan(&modelID, &rel); err != nil {
		http.NotFound(w, r)
		return
	}
	if share.Scope == "model" {
		if modelID != share.TargetID {
			http.NotFound(w, r)
			return
		}
	} else if !a.collectionContains(share.TargetID, modelID) {
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

var errShareGone = errors.New("share is expired or revoked")

func (a *App) resolveShare(token string) (ShareLink, error) {
	var s ShareLink
	err := a.db.QueryRow(`SELECT id,token,scope,target_id,COALESCE(label,''),COALESCE(expires_at,0),COALESCE(revoked_at,0),hit_count,created_at FROM share_links WHERE token = ?`, token).
		Scan(&s.ID, &s.Token, &s.Scope, &s.TargetID, &s.Label, &s.ExpiresAt, &s.RevokedAt, &s.HitCount, &s.CreatedAt)
	if err != nil {
		return s, err
	}
	now := time.Now().Unix()
	if s.RevokedAt > 0 || (s.ExpiresAt > 0 && s.ExpiresAt <= now) {
		return s, errShareGone
	}
	_, _ = a.db.Exec(`UPDATE share_links SET hit_count = hit_count + 1 WHERE id = ?`, s.ID)
	return s, nil
}

func (a *App) publicFileAllowed(s ShareLink, fileID string) (string, bool) {
	var modelID string
	if err := a.db.QueryRow(`SELECT model_id FROM files WHERE id = ?`, fileID).Scan(&modelID); err != nil {
		return "", false
	}
	if s.Scope == "model" {
		return modelID, modelID == s.TargetID
	}
	return modelID, a.collectionContains(s.TargetID, modelID)
}

func (a *App) collectionContains(collectionID, modelID string) bool {
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM collection_models WHERE collection_id = ? AND model_id = ?`, collectionID, modelID).Scan(&n)
	return n > 0
}

func publicError(w http.ResponseWriter, err error) {
	w.Header().Set("X-Robots-Tag", "noindex")
	if errors.Is(err, errShareGone) {
		writeError(w, http.StatusGone, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func randomBase62(n int) (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[idx.Int64()]
	}
	return string(out), nil
}
