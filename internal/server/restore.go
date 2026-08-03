package server

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxBackupEntries = 100000

var errBackupTooLarge = errors.New("backup exceeds the configured size limit")

type backupInspection struct {
	RestoreToken string         `json:"restoreToken"`
	Manifest     backupManifest `json:"manifest"`
}

type restoreRequest struct {
	RestoreToken string `json:"restoreToken"`
	Confirmation string `json:"confirmation"`
}

type restoreJournal struct {
	Version         int      `json:"version"`
	Token           string   `json:"token"`
	CurrentEntries  []string `json:"currentEntries"`
	RestoredEntries []string `json:"restoredEntries"`
}

func (a *App) handleInspectBackup(w http.ResponseWriter, r *http.Request) {
	max := a.maxBackupBytes()
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("backup file is required"))
		return
	}
	token, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stageRoot, err := containedName(filepath.Join(a.cfg.DataDir, ".restore", "staging"), token)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	archivePath := filepath.Join(stageRoot, "upload.fileament")
	seenFile := false
	for {
		part, partErr := mr.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid backup upload"))
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		if seenFile {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, errors.New("only one backup file is allowed"))
			return
		}
		seenFile = true
		copyErr := copyBackupUpload(archivePath, part, max)
		_ = part.Close()
		if copyErr != nil {
			if errors.Is(copyErr, errBackupTooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, errBackupTooLarge)
			} else {
				writeError(w, http.StatusInternalServerError, copyErr)
			}
			return
		}
	}
	if !seenFile {
		writeError(w, http.StatusBadRequest, errors.New("backup file is required"))
		return
	}
	manifest, err := inspectAndExtractBackup(archivePath, filepath.Join(stageRoot, "data"), max)
	_ = os.Remove(archivePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(filepath.Join(stageRoot, "manifest.json"), manifestBytes, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	keep = true
	writeJSON(w, http.StatusOK, backupInspection{RestoreToken: token, Manifest: manifest})
}

func (a *App) maxBackupBytes() int64 {
	mb := a.cfg.MaxBackupMB
	if mb <= 0 {
		mb = 8192
	}
	return mb << 20
}

func copyBackupUpload(destination string, source io.Reader, maxBytes int64) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return errBackupTooLarge
	}
	return nil
}

func inspectAndExtractBackup(archivePath, dataRoot string, maxBytes int64) (backupManifest, error) {
	var manifest backupManifest
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return manifest, errors.New("backup is not a valid Fileament archive")
	}
	defer zr.Close()
	if len(zr.File) == 0 || len(zr.File) > maxBackupEntries {
		return manifest, errors.New("backup contains an invalid number of entries")
	}
	entries := make(map[string]*zip.File, len(zr.File))
	var expanded int64
	for _, entry := range zr.File {
		if !validBackupEntryName(entry.Name) {
			return manifest, fmt.Errorf("backup contains invalid path %q", entry.Name)
		}
		if entries[entry.Name] != nil {
			return manifest, fmt.Errorf("backup contains duplicate path %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) {
			return manifest, fmt.Errorf("backup contains unsupported entry %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maxBytes) || expanded > maxBytes-int64(entry.UncompressedSize64) {
			return manifest, errors.New("backup exceeds the configured expanded size limit")
		}
		expanded += int64(entry.UncompressedSize64)
		entries[entry.Name] = entry
	}
	manifestEntry := entries["manifest.json"]
	if manifestEntry == nil || manifestEntry.UncompressedSize64 > 1<<20 {
		return manifest, errors.New("backup manifest is missing or too large")
	}
	manifestReader, err := manifestEntry.Open()
	if err != nil {
		return manifest, err
	}
	manifestContents, readErr := io.ReadAll(io.LimitReader(manifestReader, (1<<20)+1))
	manifestCloseErr := manifestReader.Close()
	if readErr != nil || manifestCloseErr != nil || len(manifestContents) > 1<<20 {
		return manifest, errors.New("backup manifest is invalid")
	}
	if err := json.Unmarshal(manifestContents, &manifest); err != nil {
		return manifest, errors.New("backup manifest is invalid")
	}
	if manifest.BackupFormatVersion != backupFormatVersion {
		return manifest, errors.New("backup format is not supported")
	}
	if manifest.DataFormatVersion > dataFormatVersion || manifest.DataFormatVersion <= 0 {
		return manifest, errors.New("backup data format is not supported")
	}
	if manifest.DatabaseVersion > schemaVersion || manifest.DatabaseVersion < 0 {
		return manifest, errors.New("backup database is newer than this Fileament build")
	}
	if entries["data/fileament.db"] == nil {
		return manifest, errors.New("backup database is missing")
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return manifest, err
	}
	actualExpanded := int64(len(manifestContents))
	for _, entry := range zr.File {
		if entry.Name == "manifest.json" {
			continue
		}
		rel := strings.TrimPrefix(entry.Name, "data/")
		if rel == "" {
			continue
		}
		destination, err := containedPath(dataRoot, rel)
		if err != nil {
			return manifest, err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return manifest, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return manifest, err
		}
		r, err := entry.Open()
		if err != nil {
			return manifest, err
		}
		mode := entry.Mode().Perm() & 0o666
		if mode == 0 {
			mode = 0o600
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = r.Close()
			return manifest, err
		}
		copied, copyErr := copyBackupEntry(out, r, maxBytes-actualExpanded)
		closeErr := out.Close()
		readCloseErr := r.Close()
		if copyErr != nil {
			return manifest, copyErr
		}
		actualExpanded += copied
		if closeErr != nil {
			return manifest, closeErr
		}
		if readCloseErr != nil {
			return manifest, readCloseErr
		}
	}
	if err := validateStagedData(dataRoot, manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func copyBackupEntry(destination io.Writer, source io.Reader, remaining int64) (int64, error) {
	copied, err := io.Copy(destination, io.LimitReader(source, remaining+1))
	if err != nil {
		return copied, err
	}
	if copied > remaining {
		return copied, errors.New("backup exceeds the configured expanded size limit")
	}
	return copied, nil
}

func validBackupEntryName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	if strings.HasSuffix(name, "/") {
		if clean+"/" != name {
			return false
		}
	} else if clean != name {
		return false
	}
	if name == "manifest.json" || name == "data/" {
		return true
	}
	if !strings.HasPrefix(name, "data/") {
		return false
	}
	rel := strings.TrimPrefix(clean, "data/")
	top := strings.SplitN(rel, "/", 2)[0]
	return top != "tmp" && top != "backups" && top != ".restore" &&
		top != "fileament.db-journal" && top != "fileament.db-shm" && top != "fileament.db-wal"
}

func validateStagedData(dataRoot string, manifest backupManifest) error {
	dbPath := filepath.Join(dataRoot, "fileament.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("backup database failed integrity validation")
	}
	foreignKeys, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return errors.New("backup database foreign keys could not be validated")
	}
	foreignKeyViolation := foreignKeys.Next()
	foreignKeyErr := foreignKeys.Err()
	_ = foreignKeys.Close()
	if foreignKeyViolation || foreignKeyErr != nil {
		return errors.New("backup database failed foreign key validation")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != manifest.DatabaseVersion {
		return errors.New("backup database version does not match its manifest")
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM models`:      manifest.Models,
		`SELECT COUNT(*) FROM files`:       manifest.Files,
		`SELECT COUNT(*) FROM collections`: manifest.Collections,
		`SELECT COUNT(*) FROM sessions`:    0,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			return errors.New("backup database counts do not match its manifest")
		}
	}
	modelsRoot := filepath.Join(dataRoot, "models")
	if err := os.MkdirAll(modelsRoot, 0o755); err != nil {
		return err
	}
	modelDirs, err := os.ReadDir(modelsRoot)
	if err != nil {
		return err
	}
	modelCount := 0
	stagedApp := &App{db: db}
	for _, dir := range modelDirs {
		if !dir.IsDir() {
			continue
		}
		sidecarPath := filepath.Join(modelsRoot, dir.Name(), "model.json")
		contents, err := os.ReadFile(sidecarPath)
		if err != nil {
			return errors.New("backup model sidecar is missing")
		}
		var model Model
		if err := json.Unmarshal(contents, &model); err != nil || model.ID != dir.Name() {
			return errors.New("backup model sidecar is invalid")
		}
		databaseModel, err := stagedApp.getModel(model.ID)
		if err != nil {
			return errors.New("backup model sidecar does not match its database")
		}
		sidecarModel, _ := json.Marshal(model)
		databaseModelJSON, _ := json.Marshal(databaseModel)
		if string(sidecarModel) != string(databaseModelJSON) {
			return errors.New("backup model sidecar does not match its database")
		}
		modelCount++
		modelRoot := filepath.Join(modelsRoot, dir.Name())
		for _, file := range model.Files {
			stored, err := containedPath(modelRoot, file.RelPath)
			if err != nil {
				return errors.New("backup model file path is invalid")
			}
			if _, err := os.Stat(stored); err != nil {
				return errors.New("backup model file is missing")
			}
			if file.SHA256 != "" {
				sum, err := shaFile(stored)
				if err != nil || sum != file.SHA256 {
					return errors.New("backup model file checksum does not match")
				}
			}
		}
		for _, image := range model.Images {
			stored, err := containedPath(modelRoot, image.RelPath)
			if err != nil {
				return errors.New("backup image path is invalid")
			}
			if _, err := os.Stat(stored); err != nil {
				return errors.New("backup image is missing")
			}
		}
	}
	if modelCount != manifest.Models {
		return errors.New("backup model sidecars do not match its manifest")
	}
	collectionsPath := filepath.Join(dataRoot, "collections.json")
	if contents, err := os.ReadFile(collectionsPath); err == nil {
		var collections []Collection
		if json.Unmarshal(contents, &collections) != nil {
			return errors.New("backup collections sidecar is invalid")
		}
		databaseCollections, err := stagedApp.listCollections()
		if err != nil {
			return errors.New("backup collections sidecar does not match its database")
		}
		sidecarCollections, _ := json.Marshal(collections)
		databaseCollectionsJSON, _ := json.Marshal(databaseCollections)
		if string(sidecarCollections) != string(databaseCollectionsJSON) {
			return errors.New("backup collections sidecar does not match its database")
		}
	} else if !errors.Is(err, os.ErrNotExist) || manifest.Collections != 0 {
		return errors.New("backup collections sidecar is missing")
	}
	return nil
}

func (a *App) handleApplyRestore(w http.ResponseWriter, r *http.Request) {
	var request restoreRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid restore request"))
		return
	}
	if request.Confirmation != "RESTORE" {
		writeError(w, http.StatusBadRequest, errors.New("type RESTORE to confirm replacement"))
		return
	}
	if _, err := containedName(filepath.Join(a.cfg.DataDir, ".restore", "staging"), request.RestoreToken); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("restore token is invalid"))
		return
	}
	if !a.maintenance.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errors.New("another maintenance operation is active"))
		return
	}
	a.resetEventStreams()
	a.stopWorkers()
	defer func() {
		if a.db == nil {
			a.recoverDatabaseAfterFailedRestore()
		}
		if a.db != nil {
			a.startWorkers()
			a.maintenance.Store(false)
		}
	}()
	a.dataMu.Lock()
	defer a.dataMu.Unlock()
	if !a.validSession(r) {
		writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	if err := a.applyRestore(request.RestoreToken); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) recoverDatabaseAfterFailedRestore() {
	if err := recoverInterruptedRestore(a.cfg.DataDir); err != nil {
		return
	}
	db, err := openDatabase(a.cfg.DataDir)
	if err != nil {
		return
	}
	a.db = db
	if err := a.initializeData(); err != nil {
		_ = db.Close()
		a.db = nil
	}
}

func (a *App) applyRestore(token string) error {
	restoreRoot := filepath.Join(a.cfg.DataDir, ".restore")
	stageRoot, err := containedName(filepath.Join(restoreRoot, "staging"), token)
	if err != nil {
		return err
	}
	manifestBytes, err := os.ReadFile(filepath.Join(stageRoot, "manifest.json"))
	if err != nil {
		return errors.New("staged restore was not found")
	}
	var manifest backupManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return errors.New("staged restore manifest is invalid")
	}
	stageData := filepath.Join(stageRoot, "data")
	if err := validateStagedData(stageData, manifest); err != nil {
		return err
	}
	currentEntries, err := managedTopLevelEntries(a.cfg.DataDir)
	if err != nil {
		return err
	}
	restoredEntries, err := managedTopLevelEntries(stageData)
	if err != nil {
		return err
	}
	if !containsString(restoredEntries, "fileament.db") {
		return errors.New("staged restore database is missing")
	}
	if _, err := a.createSafetyBackup(); err != nil {
		return fmt.Errorf("create pre-restore safety backup: %w", err)
	}
	journal := restoreJournal{
		Version:         1,
		Token:           token,
		CurrentEntries:  currentEntries,
		RestoredEntries: restoredEntries,
	}
	if err := writeRestoreJournal(a.cfg.DataDir, journal); err != nil {
		return err
	}
	rollbackRoot, err := containedName(filepath.Join(restoreRoot, "rollback"), token)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(rollbackRoot, 0o700); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(rollbackRoot)); err != nil {
		return err
	}
	if err := a.db.Close(); err != nil {
		_ = os.Remove(filepath.Join(restoreRoot, "state.json"))
		return err
	}
	a.db = nil
	applyErr := moveRestoreEntries(a.cfg.DataDir, stageData, rollbackRoot, currentEntries, restoredEntries)
	if applyErr == nil {
		a.db, applyErr = openDatabase(a.cfg.DataDir)
	}
	if applyErr == nil {
		applyErr = a.initializeData()
	}
	if applyErr == nil {
		_, applyErr = a.db.Exec(`DELETE FROM sessions`)
	}
	if applyErr == nil {
		var integrity string
		applyErr = a.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity)
		if applyErr == nil && integrity != "ok" {
			applyErr = errors.New("restored database failed integrity validation")
		}
	}
	statePath := filepath.Join(restoreRoot, "state.json")
	if applyErr == nil {
		applyErr = os.Remove(statePath)
	}
	if applyErr == nil {
		applyErr = syncDirectory(restoreRoot)
	}
	if applyErr == nil {
		_ = os.RemoveAll(rollbackRoot)
		_ = os.RemoveAll(stageRoot)
		return nil
	}
	if a.db != nil {
		_ = a.db.Close()
		a.db = nil
	}
	rollbackErr := rollbackRestoreFiles(a.cfg.DataDir, journal)
	reopenErr := error(nil)
	if rollbackErr == nil {
		a.db, reopenErr = openDatabase(a.cfg.DataDir)
		if reopenErr == nil {
			reopenErr = a.initializeData()
		}
	}
	if reopenErr != nil && a.db != nil {
		_ = a.db.Close()
		a.db = nil
	}
	return errors.Join(applyErr, rollbackErr, reopenErr)
}

func (a *App) createSafetyBackup() (string, error) {
	dir := filepath.Join(a.cfg.DataDir, "backups")
	path, _, err := a.createBackupArchive(dir)
	if err != nil {
		return "", err
	}
	name := "pre-restore-" + time.Now().UTC().Format("20060102T150405Z") + "-" + filepath.Base(strings.TrimSuffix(path, ".fileament")) + ".fileament"
	final := filepath.Join(dir, name)
	if err := os.Rename(path, final); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return final, nil
}

func managedTopLevelEntries(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{"tmp": true, "backups": true, ".restore": true}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		if _, err := containedName(root, entry.Name()); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func moveRestoreEntries(dataRoot, stageData, rollbackRoot string, currentEntries, restoredEntries []string) error {
	for _, name := range currentEntries {
		current, err := containedName(dataRoot, name)
		if err != nil {
			return err
		}
		rollback, err := containedName(rollbackRoot, name)
		if err != nil {
			return err
		}
		if err := os.Rename(current, rollback); err != nil {
			return err
		}
	}
	if err := syncDirectory(rollbackRoot); err != nil {
		return err
	}
	if err := syncDirectory(dataRoot); err != nil {
		return err
	}
	for _, name := range restoredEntries {
		staged, err := containedName(stageData, name)
		if err != nil {
			return err
		}
		current, err := containedName(dataRoot, name)
		if err != nil {
			return err
		}
		if err := os.Rename(staged, current); err != nil {
			return err
		}
	}
	if err := syncDirectory(stageData); err != nil {
		return err
	}
	if err := syncDirectory(dataRoot); err != nil {
		return err
	}
	return nil
}

func writeRestoreJournal(dataRoot string, journal restoreJournal) error {
	restoreRoot := filepath.Join(dataRoot, ".restore")
	if err := os.MkdirAll(restoreRoot, 0o700); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(restoreRoot, "state.json.tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, filepath.Join(restoreRoot, "state.json")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectory(restoreRoot)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func recoverInterruptedRestore(dataRoot string) error {
	statePath := filepath.Join(dataRoot, ".restore", "state.json")
	contents, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal restoreJournal
	if err := json.Unmarshal(contents, &journal); err != nil || journal.Version != 1 {
		return errors.New("restore recovery journal is invalid")
	}
	return rollbackRestoreFiles(dataRoot, journal)
}

func rollbackRestoreFiles(dataRoot string, journal restoreJournal) error {
	if _, err := containedName(filepath.Join(dataRoot, ".restore", "staging"), journal.Token); err != nil {
		return errors.New("restore recovery token is invalid")
	}
	rollbackRoot, err := containedName(filepath.Join(dataRoot, ".restore", "rollback"), journal.Token)
	if err != nil {
		return err
	}
	currentSet := make(map[string]bool, len(journal.CurrentEntries))
	for _, name := range journal.CurrentEntries {
		current, err := containedName(dataRoot, name)
		if err != nil {
			return err
		}
		rollback, err := containedName(rollbackRoot, name)
		if err != nil {
			return err
		}
		currentSet[name] = true
		if _, err := os.Stat(rollback); err == nil {
			if err := os.RemoveAll(current); err != nil {
				return err
			}
			if err := os.Rename(rollback, current); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range journal.RestoredEntries {
		if currentSet[name] {
			continue
		}
		current, err := containedName(dataRoot, name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(current); err != nil {
			return err
		}
	}
	if err := syncDirectory(dataRoot); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dataRoot, ".restore", "state.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(filepath.Join(dataRoot, ".restore")); err != nil {
		return err
	}
	_ = os.RemoveAll(rollbackRoot)
	stageRoot, _ := containedName(filepath.Join(dataRoot, ".restore", "staging"), journal.Token)
	_ = os.RemoveAll(stageRoot)
	return nil
}
