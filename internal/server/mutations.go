package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/TechHutTV/fileament/internal/ids"
)

type mutationState struct {
	Version            int    `json:"version"`
	Phase              string `json:"phase"`
	ModelID            string `json:"modelId,omitempty"`
	ModelExisted       bool   `json:"modelExisted"`
	CollectionsExisted bool   `json:"collectionsExisted"`
}

var errMutationRecoveryRequired = errors.New("storage recovery is required")

type dataMutation struct {
	app   *App
	root  string
	state mutationState
}

func (a *App) beginMutation(modelID string) (*dataMutation, error) {
	a.mutationMu.Lock()
	ready := false
	defer func() {
		if !ready {
			a.mutationMu.Unlock()
		}
	}()
	if a.maintenance.Load() {
		return nil, errMutationRecoveryRequired
	}
	if modelID != "" && !validStorageID(modelID) {
		return nil, errInvalidPath
	}
	parent := filepath.Join(a.cfg.DataDir, ".mutations")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	id := ids.New()
	m := &dataMutation{app: a, root: filepath.Join(parent, "preparing-"+id), state: mutationState{Version: 1, Phase: "prepared", ModelID: modelID}}
	if err := os.Mkdir(m.root, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if !ready {
			_ = m.cleanup()
		}
	}()
	if modelID != "" {
		source, err := modelRootPath(a.cfg.DataDir, modelID)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(source); err == nil {
			if err := snapshotTree(source, filepath.Join(m.root, "model")); err != nil {
				return nil, err
			}
			m.state.ModelExisted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	collections := filepath.Join(a.cfg.DataDir, "collections.json")
	if _, err := os.Lstat(collections); err == nil {
		if err := snapshotFile(collections, filepath.Join(m.root, "collections.json")); err != nil {
			return nil, err
		}
		m.state.CollectionsExisted = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := m.validateBefore(); err != nil {
		return nil, err
	}
	if err := syncTree(m.root); err != nil {
		return nil, err
	}
	if err := m.writeState("prepared"); err != nil {
		return nil, err
	}
	published := filepath.Join(parent, id)
	if err := os.Rename(m.root, published); err != nil {
		return nil, err
	}
	m.root = published
	if err := errors.Join(syncDirectory(parent), syncDirectory(a.cfg.DataDir)); err != nil {
		return nil, err
	}
	ready = true
	return m, nil
}

func (m *dataMutation) writeState(phase string) error {
	if err := m.app.mutationStep("journal-" + phase); err != nil {
		return err
	}
	next := m.state
	next.Phase = phase
	contents, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(m.root, "state.json"), contents, 0o600); err != nil {
		return err
	}
	m.state = next
	return nil
}

func (m *dataMutation) finish(operationErr error) error {
	defer m.app.mutationMu.Unlock()
	if operationErr == nil {
		operationErr = m.verifyAndSync()
	}
	if operationErr == nil {
		operationErr = m.app.mutationStep("before-commit")
	}
	if operationErr == nil {
		if err := m.writeState("committed"); err != nil {
			// The rename may have succeeded before the directory sync failed.
			m.app.requireMutationRecovery()
			return errors.Join(errMutationRecoveryRequired, fmt.Errorf("mutation commit needs recovery: %w", err))
		}
		if err := m.app.mutationStep("committed"); err != nil {
			m.app.requireMutationRecovery()
			return errors.Join(errMutationRecoveryRequired, err)
		}
		if err := m.cleanup(); err != nil {
			m.app.requireMutationRecovery()
			return errors.Join(errMutationRecoveryRequired, fmt.Errorf("mutation cleanup needs recovery: %w", err))
		}
		return nil
	}
	if err := m.rollback(); err != nil {
		m.app.requireMutationRecovery()
		return errors.Join(errMutationRecoveryRequired, operationErr, fmt.Errorf("mutation rollback needs recovery: %w", err))
	}
	return operationErr
}

func (a *App) requireMutationRecovery() {
	a.mutationRecovery.Store(true)
	a.maintenance.Store(true)
	a.resetEventStreams()
}

func (m *dataMutation) verifyAndSync() error {
	if err := m.app.mutationStep("sync"); err != nil {
		return err
	}
	if m.state.ModelID != "" {
		root, err := modelRootPath(m.app.cfg.DataDir, m.state.ModelID)
		if err != nil {
			return err
		}
		model, err := m.app.getModel(m.state.ModelID)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				return errors.New("deleted model data remains on disk")
			}
		} else if err != nil {
			return err
		} else {
			var sidecar Model
			if err := readMutationJSON(filepath.Join(root, "model.json"), &sidecar); err != nil {
				return err
			}
			if !equalJSON(model, sidecar) {
				return errors.New("model sidecar does not match its index")
			}
			if err := validateSidecarModel(sidecar); err != nil {
				return err
			}
			if err := validateModelAssets(root, sidecar); err != nil {
				return err
			}
			if err := syncTree(root); err != nil {
				return err
			}
		}
		if err := syncDirectory(filepath.Join(m.app.cfg.DataDir, "models")); err != nil {
			return err
		}
	}
	collections, err := m.app.listCollections()
	if err != nil {
		return err
	}
	var sidecar []Collection
	path := filepath.Join(m.app.cfg.DataDir, "collections.json")
	if err := readMutationJSON(path, &sidecar); err != nil {
		if !errors.Is(err, os.ErrNotExist) || len(collections) != 0 {
			return err
		}
	} else if !equalJSON(collections, sidecar) {
		return errors.New("collection sidecar does not match its index")
	} else if err := syncRegularFile(path); err != nil {
		return err
	}
	return syncDirectory(m.app.cfg.DataDir)
}

func (m *dataMutation) rollback() error {
	if err := m.app.mutationStep("rollback"); err != nil {
		return err
	}
	if err := m.validateBefore(); err != nil {
		return err
	}
	if m.state.ModelID != "" {
		target, err := modelRootPath(m.app.cfg.DataDir, m.state.ModelID)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if m.state.ModelExisted {
			if err := snapshotTree(filepath.Join(m.root, "model"), target); err != nil {
				return err
			}
			if err := syncTree(target); err != nil {
				return err
			}
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	collections := filepath.Join(m.app.cfg.DataDir, "collections.json")
	if m.state.CollectionsExisted {
		contents, err := os.ReadFile(filepath.Join(m.root, "collections.json"))
		if err != nil {
			return err
		}
		if err := atomicWriteFile(collections, contents, 0o644); err != nil {
			return err
		}
	} else if err := os.Remove(collections); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(m.app.cfg.DataDir); err != nil {
		return err
	}
	if err := m.app.mutationStep("restored-files"); err != nil {
		return err
	}
	if err := m.app.rebuildFromSidecars(); err != nil {
		return err
	}
	if err := m.app.rebuildCollectionsFromSidecar(); err != nil {
		return err
	}
	if err := m.writeState("rolled-back"); err != nil {
		return err
	}
	return m.cleanup()
}

func (m *dataMutation) validateBefore() error {
	for path, existed := range map[string]bool{
		filepath.Join(m.root, "model"):            m.state.ModelExisted,
		filepath.Join(m.root, "collections.json"): m.state.CollectionsExisted,
	} {
		_, err := os.Lstat(path)
		if !existed && !errors.Is(err, os.ErrNotExist) {
			return errors.New("mutation snapshot does not match its presence flags")
		}
		if existed && err != nil {
			return err
		}
	}
	if m.state.ModelExisted {
		root := filepath.Join(m.root, "model")
		var model Model
		if err := readMutationJSON(filepath.Join(root, "model.json"), &model); err != nil {
			return err
		}
		if model.ID != m.state.ModelID {
			return errors.New("mutation snapshot model identifier does not match")
		}
		if err := validateSidecarModel(model); err != nil {
			return err
		}
		if err := validateModelAssets(root, model); err != nil {
			return err
		}
	}
	if m.state.CollectionsExisted {
		var collections []Collection
		if err := readMutationJSON(filepath.Join(m.root, "collections.json"), &collections); err != nil {
			return err
		}
	}
	return nil
}

func validateModelAssets(root string, model Model) error {
	type asset struct {
		path    string
		derived bool
	}
	var paths []asset
	for _, file := range model.Files {
		paths = append(paths, asset{path: file.RelPath})
		if file.ThumbPath != "" {
			paths = append(paths, asset{path: file.ThumbPath, derived: true})
		}
	}
	for _, image := range model.Images {
		paths = append(paths, asset{path: image.RelPath})
	}
	if model.PrimaryThumb != "" {
		paths = append(paths, asset{path: "thumbs/" + model.PrimaryThumb, derived: true})
	}
	for _, asset := range paths {
		path, err := containedPath(root, asset.path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if asset.derived && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("model references a non-regular asset")
		}
	}
	return nil
}

func (a *App) mutationStep(step string) error {
	if a.mutationFault != nil {
		return a.mutationFault(step)
	}
	return nil
}

func (a *App) removeStorageFile(path string) error {
	if err := a.mutationStep("remove"); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *dataMutation) cleanup() error {
	parent := filepath.Dir(m.root)
	target := filepath.Join(parent, "cleanup-"+ids.New())
	if err := os.Rename(m.root, target); err != nil {
		return err
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func (a *App) recoverMutations() error {
	root := filepath.Join(a.cfg.DataDir, ".mutations")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return errors.New("invalid mutation recovery directory")
		}
		name := entry.Name()
		if (strings.HasPrefix(name, "preparing-") && validStorageID(strings.TrimPrefix(name, "preparing-"))) || (strings.HasPrefix(name, "cleanup-") && validStorageID(strings.TrimPrefix(name, "cleanup-"))) {
			if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
				return err
			}
			if err := syncDirectory(root); err != nil {
				return err
			}
			continue
		}
		if !validStorageID(name) {
			return errors.New("invalid mutation recovery directory")
		}
		m := &dataMutation{app: a, root: filepath.Join(root, entry.Name())}
		if err := readMutationJSON(filepath.Join(m.root, "state.json"), &m.state); err != nil {
			return err
		}
		if m.state.Version != 1 || (m.state.ModelID != "" && !validStorageID(m.state.ModelID)) || (m.state.ModelID == "" && m.state.ModelExisted) {
			return errors.New("invalid mutation recovery state")
		}
		switch m.state.Phase {
		case "prepared":
			if err := m.rollback(); err != nil {
				return err
			}
		case "committed", "rolled-back":
			if err := m.cleanup(); err != nil {
				return err
			}
		default:
			return errors.New("unknown mutation recovery phase")
		}
	}
	return nil
}

func readMutationJSON(path string, value any) error {
	contents, err := readFileLimited(path, maxBackupSidecarBytes)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, value)
}

func equalJSON(left, right any) bool {
	l, err := json.Marshal(left)
	if err != nil {
		return false
	}
	r, err := json.Marshal(right)
	return err == nil && string(l) == string(r)
}

func snapshotTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(destination, info.Mode().Perm())
		}
		return snapshotFile(path, destination)
	})
}

func snapshotFile(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("mutation snapshot contains a non-regular file")
	}
	if err := os.Link(source, target); err == nil {
		return nil
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	copyErr := output.Chmod(info.Mode().Perm())
	if copyErr == nil {
		_, copyErr = io.Copy(output, input)
	}
	return errors.Join(copyErr, output.Close(), input.Close())
}

func syncRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("cannot sync a non-regular storage file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func syncTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		return syncRegularFile(path)
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncDirectory(directories[i]); err != nil {
			return err
		}
	}
	return nil
}
