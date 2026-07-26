package server

import (
	"archive/zip"
	"crypto/sha256"
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
	r.With(a.requireAuth).Post("/api/models", a.handleCreateModel)
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
	if _, err := tx.Exec(`INSERT INTO models_fts(rowid, title, description, tags) VALUES((SELECT rowid FROM models WHERE id = ?), ?, ?, '')`, model.ID, model.Title, model.Description); err != nil {
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
