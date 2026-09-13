package server

import (
	"net/http"
	"os"
	"path"

	"github.com/TechHutTV/fileament/internal/storage"
)

func (a *App) openModelAsset(modelID, rel string) (*os.File, error) {
	if !validStorageID(modelID) || !(validAssetPath(rel, "files") || validAssetPath(rel, "images") || validAssetPath(rel, "thumbs")) {
		return nil, errInvalidPath
	}
	return storage.OpenRegular(a.dataRoot, "models/"+modelID+"/"+rel)
}

func (a *App) serveModelAsset(w http.ResponseWriter, r *http.Request, modelID, rel string) {
	file, err := a.openModelAsset(modelID, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, path.Base(rel), info.ModTime(), file)
}
