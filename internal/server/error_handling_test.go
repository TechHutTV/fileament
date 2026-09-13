package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternalErrorsDoNotExposeStorageDetails(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		rec := httptest.NewRecorder()
		writeError(rec, status, errors.New("/data/private.db: password=secret token=hidden"))
		if rec.Code != status {
			t.Fatalf("status=%d, got %d", status, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != "internal server error" || strings.Contains(rec.Body.String(), "private.db") || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "hidden") {
			t.Fatalf("internal details leaked: %s", rec.Body.String())
		}
	}
}

func TestClientErrorsRemainActionable(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, errors.New("filename is invalid"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "filename is invalid") {
		t.Fatalf("client error was not preserved: %s", rec.Body.String())
	}
}

func TestCollectionAndThumbnailScopeChecksFailClosedOnClosedDatabase(t *testing.T) {
	app := newTestApp(t)
	if err := app.db.Close(); err != nil {
		t.Fatal(err)
	}
	if app.collectionContains(t.Context(), "collection", "model") {
		t.Fatal("collection membership succeeded after database close")
	}
	if app.thumbAllowed(t.Context(), "model", "card.png") {
		t.Fatal("thumbnail scope succeeded after database close")
	}
}
