package server

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
)

var errInvalidPath = errors.New("invalid path")

func containedPath(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return "", errInvalidPath
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errInvalidPath
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	pathAbs, err := filepath.Abs(filepath.Join(rootAbs, clean))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || !filepath.IsLocal(relative) {
		return "", errInvalidPath
	}
	return pathAbs, nil
}

func containedName(root, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", errInvalidPath
	}
	return containedPath(root, name)
}

func validStorageID(value string) bool {
	id, err := ulid.ParseStrict(value)
	return err == nil && id.String() == value
}

func modelRootPath(dataDir, modelID string) (string, error) {
	if !validStorageID(modelID) {
		return "", errInvalidPath
	}
	return containedName(filepath.Join(dataDir, "models"), modelID)
}

func validAssetPath(rel, directory string) bool {
	name := strings.TrimPrefix(rel, directory+"/")
	_, err := containedName(directory, name)
	return err == nil && rel == directory+"/"+name
}

func validateSidecarModel(model Model) error {
	if !validStorageID(model.ID) {
		return errors.New("invalid model identifier")
	}
	if model.PrimaryThumb != "" && model.PrimaryThumb != "card.png" && model.PrimaryThumb != "card.jpg" {
		return errors.New("invalid primary thumbnail")
	}
	fileIDs := make(map[string]bool)
	imageIDs := make(map[string]bool)
	paths := make(map[string]bool)
	for _, file := range model.Files {
		if !validStorageID(file.ID) || file.ModelID != model.ID || fileIDs[file.ID] {
			return errors.New("invalid file identifier or ownership")
		}
		fileIDs[file.ID] = true
		if !validAssetPath(file.RelPath, "files") || paths[file.RelPath] {
			return errors.New("invalid model file path")
		}
		paths[file.RelPath] = true
		if _, err := containedName(".", file.Filename); err != nil {
			return errors.New("invalid model filename")
		}
		if file.Format != "stl" && file.Format != "obj" && file.Format != "3mf" {
			return errors.New("unsupported model file format")
		}
		if !strings.EqualFold(filepath.Ext(file.RelPath), "."+file.Format) || !strings.EqualFold(filepath.Ext(file.Filename), "."+file.Format) {
			return errors.New("model file extension does not match its format")
		}
		if file.ThumbPath != "" && file.ThumbPath != "thumbs/"+file.ID+".png" && file.ThumbPath != "thumbs/"+file.ID+".jpg" {
			return errors.New("invalid file thumbnail path")
		}
	}
	for _, image := range model.Images {
		if !validStorageID(image.ID) || image.ModelID != model.ID || imageIDs[image.ID] {
			return errors.New("invalid image identifier or ownership")
		}
		imageIDs[image.ID] = true
		if !validAssetPath(image.RelPath, "images") || classifyExt(image.RelPath) != "image" || paths[image.RelPath] {
			return errors.New("invalid image path")
		}
		paths[image.RelPath] = true
	}
	return nil
}
