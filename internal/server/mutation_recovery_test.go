package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
)

func TestFailedAppendDoesNotLeavePublishedFiles(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "original.stl", "Original")
	root := filepath.Join(app.cfg.DataDir, "models", model.ID)
	before, err := os.ReadFile(filepath.Join(root, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`CREATE TRIGGER reject_extra_file BEFORE INSERT ON files WHEN NEW.filename = 'extra.stl' BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	body, contentType := multipartFile(t, "extra.stl", []byte(validSTL()))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/files", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("append=%d %s", rec.Code, rec.Body.String())
	}
	files, err := os.ReadDir(filepath.Join(root, "files"))
	if err != nil || len(files) != 1 || files[0].Name() != "original.stl" {
		t.Fatalf("failed append left files=%v err=%v", files, err)
	}
	after, err := os.ReadFile(filepath.Join(root, "model.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("failed append changed sidecar: err=%v", err)
	}
	stored, err := app.getModel(model.ID)
	if err != nil || len(stored.Files) != 1 || stored.TotalBytes != model.TotalBytes {
		t.Fatalf("failed append changed model: %+v err=%v", stored, err)
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
}

type mutationFixture struct {
	app        *App
	cookie     *http.Cookie
	model      Model
	other      Model
	collection Collection
	share      ShareLink
}

func newMutationFixture(t *testing.T) mutationFixture {
	t.Helper()
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	body, contentType := multipartZip(t, map[string]string{
		"alpha.stl": validSTL(), "beta.stl": strings.ReplaceAll(validSTL(), "vertex 1 0 0", "vertex 3 0 0"), "photo.png": "image",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fixture upload=%d %s", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	other := uploadSTLModel(t, app, cookie, "other.stl", "Other")
	for i := 0; i < 3; i++ {
		if err := app.processNextThumbnail(); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	model, err = app.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err = app.getModel(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec = serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/collections", `{"name":"Original collection"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("fixture collection=%d %s", rec.Code, rec.Body.String())
	}
	var collection Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{model.ID, other.ID} {
		rec = serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodPut, "/api/collections/"+collection.ID+"/models/"+id, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("fixture membership=%d %s", rec.Code, rec.Body.String())
		}
	}
	rec = serveMutationRequest(app, cookie, jsonReq(http.MethodPatch, "/api/collections/"+collection.ID, `{"name":"Original collection","coverModelId":"`+model.ID+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("fixture cover=%d %s", rec.Code, rec.Body.String())
	}
	rec = serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/shares", `{"scope":"collection","targetId":"`+collection.ID+`"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("fixture share=%d %s", rec.Code, rec.Body.String())
	}
	var share ShareLink
	if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
		t.Fatal(err)
	}
	return mutationFixture{app: app, cookie: cookie, model: model, other: other, collection: collection, share: share}
}

func serveMutationRequest(app *App, cookie *http.Cookie, req *http.Request) *httptest.ResponseRecorder {
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	return rec
}

type librarySnapshot struct {
	Metadata string
	Files    map[string][32]byte
}

func snapshotLibrary(t *testing.T, app *App) librarySnapshot {
	t.Helper()
	rows, err := app.db.Query(`SELECT id FROM models ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	var models []Model
	for _, id := range ids {
		model, err := app.getModel(id)
		if err != nil {
			t.Fatal(err)
		}
		models = append(models, model)
	}
	collections, err := app.listCollections()
	if err != nil {
		t.Fatal(err)
	}
	tagRows, err := app.db.Query(`SELECT name FROM tags ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for tagRows.Next() {
		var tag string
		if err := tagRows.Scan(&tag); err != nil {
			t.Fatal(err)
		}
		tags = append(tags, tag)
	}
	if err := errors.Join(tagRows.Err(), tagRows.Close()); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(struct {
		Models      []Model
		Collections []Collection
		Tags        []string
	}{models, collections, tags})
	if err != nil {
		t.Fatal(err)
	}
	out := librarySnapshot{Metadata: string(metadata), Files: map[string][32]byte{}}
	for _, name := range []string{"models", "collections.json"} {
		err := filepath.WalkDir(filepath.Join(app.cfg.DataDir, name), func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(app.cfg.DataDir, path)
			if err != nil {
				return err
			}
			out.Files[rel] = sha256.Sum256(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestMutationsRollBackSidecarsFilesAndIndexOnFailure(t *testing.T) {
	operations := []string{"create-model", "append", "metadata", "rename", "delete-file", "delete-image", "thumbnail", "delete-model", "create-collection", "edit-collection", "delete-collection", "remove-member", "reorder"}
	for _, operation := range operations {
		steps := []string{"before-commit"}
		if operation == "delete-model" || operation == "delete-file" || operation == "delete-image" {
			steps = append(steps, "remove")
		}
		if operation == "append" || operation == "delete-model" || operation == "remove-member" {
			sidecar := "model-sidecar"
			if operation != "append" {
				sidecar = "collections-sidecar"
			}
			steps = append(steps, "journal-prepared", sidecar, "sync")
		}
		for _, step := range steps {
			t.Run(operation+"/"+step, func(t *testing.T) {
				fixture := newMutationFixture(t)
				app, model, collection := fixture.app, fixture.model, fixture.collection
				before := snapshotLibrary(t, app)
				var req *http.Request
				switch operation {
				case "create-model", "append":
					path := "/api/models"
					if operation == "append" {
						path += "/" + model.ID + "/files"
					}
					body, contentType := multipartZip(t, map[string]string{"extra.stl": validSTL(), "extra.png": "extra image"})
					req = httptest.NewRequest(http.MethodPost, path, body)
					req.Header.Set("Content-Type", contentType)
				case "metadata":
					req = jsonReq(http.MethodPatch, "/api/models/"+model.ID, `{"title":"Changed","tags":["new-tag"]}`)
				case "rename":
					req = jsonReq(http.MethodPatch, "/api/models/"+model.ID+"/files/"+model.Files[0].ID, `{"filename":"renamed.stl"}`)
				case "delete-file":
					req = httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/files/"+model.Files[0].ID, nil)
				case "delete-image":
					req = httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/images/"+model.Images[0].ID, nil)
				case "thumbnail":
					req = jsonReq(http.MethodPut, "/api/models/"+model.ID+"/thumb", `{"fileId":"`+model.Files[1].ID+`"}`)
				case "delete-model":
					req = httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID, nil)
				case "create-collection":
					req = jsonReq(http.MethodPost, "/api/collections", `{"name":"Another collection"}`)
				case "edit-collection":
					req = jsonReq(http.MethodPatch, "/api/collections/"+collection.ID, `{"name":"Changed collection"}`)
				case "delete-collection":
					req = httptest.NewRequest(http.MethodDelete, "/api/collections/"+collection.ID, nil)
				case "remove-member":
					req = httptest.NewRequest(http.MethodDelete, "/api/collections/"+collection.ID+"/models/"+model.ID, nil)
				case "reorder":
					req = jsonReq(http.MethodPut, "/api/collections/"+collection.ID+"/order", `{"modelIds":["`+fixture.other.ID+`","`+model.ID+`"]}`)
				}
				hit := false
				app.mutationFault = func(point string) error {
					if point == step {
						hit = true
						return errors.New("injected " + step + " failure")
					}
					return nil
				}
				rec := serveMutationRequest(app, fixture.cookie, req)
				app.mutationFault = nil
				if rec.Code != http.StatusInternalServerError || !hit || app.maintenance.Load() {
					t.Fatalf("status=%d hit=%v maintenance=%v body=%s", rec.Code, hit, app.maintenance.Load(), rec.Body.String())
				}
				if after := snapshotLibrary(t, app); !reflect.DeepEqual(before, after) {
					t.Fatalf("failed mutation changed library:\nbefore=%+v\nafter=%+v", before, after)
				}
				public := httptest.NewRecorder()
				app.Router().ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/public/"+fixture.share.Token+"/files/"+model.Files[0].ID, nil))
				if public.Code != http.StatusOK {
					t.Fatalf("failed mutation changed public membership: %d", public.Code)
				}
				inspectBackup(t, app, fixture.cookie, downloadBackup(t, app, fixture.cookie))
				assertMutationRestarts(t, app, before)
			})
		}
	}
}

func TestMutationPanicRollsBackBeforeReturningControl(t *testing.T) {
	for _, response := range []bool{false, true} {
		t.Run(fmt.Sprint("response=", response), func(t *testing.T) {
			fixture := newMutationFixture(t)
			app := fixture.app
			before := snapshotLibrary(t, app)
			func() {
				defer func() {
					if got := recover(); got != "interrupted" {
						t.Fatalf("recovered %v", got)
					}
				}()
				if response {
					_, finish, err := app.beginMutationResponse(httptest.NewRecorder(), fixture.model.ID)
					if err != nil {
						t.Fatal(err)
					}
					defer finish()
				} else {
					mutation, err := app.beginMutation(fixture.model.ID)
					if err != nil {
						t.Fatal(err)
					}
					defer mutation.finishOnReturn(&err)
				}
				changed := fixture.model
				changed.Title = "Interrupted edit"
				if err := app.updateModel(changed); err != nil {
					t.Fatal(err)
				}
				if err := app.writeSidecar(changed); err != nil {
					t.Fatal(err)
				}
				panic("interrupted")
			}()
			if got := snapshotLibrary(t, app); !reflect.DeepEqual(before, got) {
				t.Fatal("panic left partial changes")
			}
			if !app.mutationMu.TryLock() {
				t.Fatal("panic retained mutation lock")
			}
			app.mutationMu.Unlock()
			assertMutationRestarts(t, app, before)
		})
	}
}

func assertMutationRestarts(t *testing.T, app *App, before librarySnapshot) {
	t.Helper()
	cfg, web := app.cfg, app.webFS
	for _, fresh := range []bool{false, true} {
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		if fresh {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := os.Remove(filepath.Join(cfg.DataDir, "fileament.db") + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
		}
		var err error
		app, err = New(cfg, web)
		if err != nil {
			t.Fatalf("restart fresh=%v: %v", fresh, err)
		}
		if got := snapshotLibrary(t, app); !reflect.DeepEqual(got, before) {
			t.Fatalf("restart fresh=%v changed library:\nbefore=%+v\nafter=%+v", fresh, before, got)
		}
		journals, err := os.ReadDir(filepath.Join(cfg.DataDir, ".mutations"))
		if err != nil || len(journals) != 0 {
			t.Fatalf("pending journals=%v err=%v", journals, err)
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMutationRecoveryRetainsSnapshotUntilRollbackCompletes(t *testing.T) {
	for _, stage := range []string{"rollback", "restored-files", "journal-rolled-back"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newMutationFixture(t)
			app := fixture.app
			before := snapshotLibrary(t, app)
			app.mutationFault = func(point string) error {
				if point == "before-commit" || point == stage {
					return fmt.Errorf("injected %s interruption", point)
				}
				return nil
			}
			rec := serveMutationRequest(app, fixture.cookie, httptest.NewRequest(http.MethodDelete, "/api/models/"+fixture.model.ID, nil))
			if rec.Code != http.StatusServiceUnavailable || !app.maintenance.Load() {
				t.Fatalf("status=%d maintenance=%v body=%s", rec.Code, app.maintenance.Load(), rec.Body.String())
			}
			app.mutationFault = nil
			health := httptest.NewRecorder()
			app.Router().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if health.Code != http.StatusServiceUnavailable {
				t.Fatalf("health after failed recovery=%d", health.Code)
			}
			assertMutationRestarts(t, app, before)
		})
	}
}

func TestPreparedMutationRecoversAfterRestartWithEitherIndex(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh-%v", fresh), func(t *testing.T) {
			fixture := newMutationFixture(t)
			app, model := fixture.app, fixture.model
			before := snapshotLibrary(t, app)
			if _, err := app.beginMutation(model.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := app.db.Exec(`DELETE FROM models WHERE id = ?`, model.ID); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(app.cfg.DataDir, "models", model.ID)); err != nil {
				t.Fatal(err)
			}
			if err := app.writeCollectionsSidecar(); err != nil {
				t.Fatal(err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			if fresh {
				if err := os.Remove(filepath.Join(app.cfg.DataDir, "fileament.db")); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := New(app.cfg, app.webFS)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if got := snapshotLibrary(t, recovered); !reflect.DeepEqual(before, got) {
				t.Fatalf("interrupted deletion was not restored: before=%+v after=%+v", before, got)
			}
			inspectBackup(t, recovered, loginCookie(t, recovered, "password-password"), downloadBackup(t, recovered, loginCookie(t, recovered, "password-password")))
		})
	}
}

func TestMutationCommitMarkerDecidesRecovery(t *testing.T) {
	for _, stage := range []string{"journal-committed", "committed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newMutationFixture(t)
			app := fixture.app
			before := snapshotLibrary(t, app)
			app.mutationFault = func(point string) error {
				if point == stage {
					return errors.New("injected commit interruption")
				}
				return nil
			}
			rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"title":"Committed title"}`))
			app.mutationFault = nil
			if rec.Code != http.StatusServiceUnavailable || !app.maintenance.Load() {
				t.Fatalf("status=%d maintenance=%v body=%s", rec.Code, app.maintenance.Load(), rec.Body.String())
			}
			want := before
			if stage == "committed" {
				want = snapshotLibrary(t, app)
				if reflect.DeepEqual(want, before) {
					t.Fatal("commit did not change the model")
				}
			}
			assertMutationRestarts(t, app, want)
		})
	}
}

func TestMissingMutationSnapshotFailsBeforeRemovingActiveData(t *testing.T) {
	for _, missing := range []string{"model/model.json", "state.json"} {
		t.Run(missing, func(t *testing.T) {
			fixture := newMutationFixture(t)
			app := fixture.app
			before, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", fixture.model.ID, "model.json"))
			if err != nil {
				t.Fatal(err)
			}
			mutation, err := app.beginMutation(fixture.model.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(mutation.root, missing)); err != nil {
				t.Fatal(err)
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			recovered, err := New(app.cfg, app.webFS)
			if err == nil {
				_ = recovered.Close()
				t.Fatal("missing rollback evidence accepted")
			}
			after, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", fixture.model.ID, "model.json"))
			if err != nil || string(after) != string(before) {
				t.Fatalf("recovery damaged the active model before validating its snapshot: %v", err)
			}
		})
	}
}

func TestIncompleteMutationPreparationAndCleanupAreDisposable(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	before := snapshotLibrary(t, app)
	for _, prefix := range []string{"preparing-", "cleanup-"} {
		root := filepath.Join(app.cfg.DataDir, ".mutations", prefix+ids.New())
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "partial"), []byte("partial snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertMutationRestarts(t, app, before)
}

func TestSidecarReconciliationRemovesStaleIndexRows(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	before := snapshotLibrary(t, app)
	for _, query := range []string{
		`INSERT INTO models(id,title,created_at,updated_at) VALUES('stale-model','Stale model',1,1)`,
		`INSERT INTO collections(id,name,slug,created_at) VALUES('stale-collection','Stale','stale',1)`,
		`INSERT INTO tags(id,name,slug) VALUES('stale-tag','Stale tag','stale-tag')`,
	} {
		if _, err := app.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.db.Exec(`INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes) VALUES('stale-file',?,'stale.stl','files/stale.stl','stl',1)`, fixture.model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`INSERT INTO images(id,model_id,rel_path) VALUES('stale-image',?,'images/stale.png')`, fixture.model.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES('stale-job','thumbnail','stale-file','pending',1)`); err != nil {
		t.Fatal(err)
	}
	assertMutationRestarts(t, app, before)
}

func TestMissingModelSidecarStopsStartupWithoutDiscardingIndex(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	root := filepath.Join(app.cfg.DataDir, "models", fixture.model.ID)
	if err := os.Remove(filepath.Join(root, "model.json")); err != nil {
		t.Fatal(err)
	}
	if err := app.rebuildFromSidecars(); err == nil {
		t.Fatal("missing durable model sidecar accepted")
	}
	if _, err := app.getModel(fixture.model.ID); err != nil {
		t.Fatalf("index discarded before metadata recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, fixture.model.Files[0].RelPath)); err != nil {
		t.Fatalf("original mesh discarded: %v", err)
	}
}

func TestSharedTagLinkPreservesOtherModelsDurableLabel(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	for id, tag := range map[string]string{fixture.model.ID: "Tools", fixture.other.ID: "Tools"} {
		rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+id, `{"tags":["`+tag+`"]}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("tag=%d %s", rec.Code, rec.Body.String())
		}
	}
	rec := serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.other.ID, `{"tags":["tools"]}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("alias=%d %s", rec.Code, rec.Body.String())
	}
	model, err := app.getModel(fixture.model.ID)
	if err != nil || len(model.Tags) != 1 || model.Tags[0] != "Tools" {
		t.Fatalf("another model's tag changed: %+v err=%v", model, err)
	}
	var sidecar Model
	if err := readMutationJSON(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"), &sidecar); err != nil || !equalJSON(model, sidecar) {
		t.Fatalf("other model and sidecar differ: %v", err)
	}
	inspectBackup(t, app, fixture.cookie, downloadBackup(t, app, fixture.cookie))
	assertMutationRestarts(t, app, snapshotLibrary(t, app))
}

func TestRestoreCannotClearAnOverlappingMutationRecoveryFailure(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	before := snapshotLibrary(t, app)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	app.mutationFault = func(step string) error {
		if step == "before-commit" {
			close(entered)
			<-release
			return errors.New("injected commit failure")
		}
		if step == "rollback" {
			return errors.New("injected rollback failure")
		}
		return nil
	}
	mutationResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		mutationResult <- serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPatch, "/api/models/"+fixture.model.ID, `{"title":"Interrupted edit"}`))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not reach commit")
	}
	restoreResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		restoreResult <- serveMutationRequest(app, fixture.cookie, jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"unused","confirmation":"RESTORE"}`))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !app.maintenance.Load() {
		if time.Now().After(deadline) {
			t.Fatal("restore did not enter maintenance")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	released = true
	for name, result := range map[string]<-chan *httptest.ResponseRecorder{"mutation": mutationResult, "restore": restoreResult} {
		select {
		case response := <-result:
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s status=%d body=%s", name, response.Code, response.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	if !app.maintenance.Load() || !app.mutationRecovery.Load() {
		t.Fatal("restore cleared unresolved mutation maintenance")
	}
	app.mutationFault = nil
	assertMutationRestarts(t, app, before)
}

func TestDataAndBackupHandlersRecheckMaintenanceAfterLock(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	app.requireMutationRecovery()
	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	req.AddCookie(fixture.cookie)
	rec := httptest.NewRecorder()
	app.handleCreateBackup(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("backup bypassed maintenance: %d", rec.Code)
	}
	called := false
	handler := app.dataAccessMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("data request bypassed maintenance: %d called=%v", rec.Code, called)
	}
}

func TestInitialCollectionFailureRestoresAbsentSidecar(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	before := snapshotLibrary(t, app)
	app.mutationFault = func(step string) error {
		if step == "collections-sidecar" {
			return errors.New("injected sidecar failure")
		}
		return nil
	}
	rec := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/collections", `{"name":"Failed collection"}`))
	app.mutationFault = nil
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := snapshotLibrary(t, app); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed creation changed library: %+v", got)
	}
	assertMutationRestarts(t, app, before)
}

func TestThumbnailPublicationFailureRestoresPriorFiles(t *testing.T) {
	fixture := newMutationFixture(t)
	app := fixture.app
	before := snapshotLibrary(t, app)
	if _, err := app.db.Exec(`UPDATE jobs SET status = 'pending', created_at = 0 WHERE file_id = ?`, fixture.model.Files[0].ID); err != nil {
		t.Fatal(err)
	}
	events, _ := app.subscribeEventStream()
	defer app.unsubscribeEvents(events)
	app.mutationFault = func(step string) error {
		if step == "model-sidecar" {
			return errors.New("injected thumbnail publication failure")
		}
		return nil
	}
	err := app.processNextThumbnail()
	app.mutationFault = nil
	if err == nil || app.maintenance.Load() {
		t.Fatalf("error=%v maintenance=%v", err, app.maintenance.Load())
	}
	if got := snapshotLibrary(t, app); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed thumbnail changed library: before=%+v after=%+v", before, got)
	}
	select {
	case event := <-events:
		if event.Status != "failed" || event.ThumbPath != "" || event.ModelID != fixture.model.ID {
			t.Fatalf("failed thumbnail published an invalid state: %+v", event)
		}
	default:
		t.Fatal("failed thumbnail did not publish its failure state")
	}
	assertMutationRestarts(t, app, before)
}
