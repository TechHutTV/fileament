package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPasswordChangeRevokesPreviousSessions(t *testing.T) {
	app := newAuthedTestApp(t)
	first := loginCookie(t, app, "password-password")
	second := loginCookie(t, app, "password-password")
	app.cfg.BaseURL = "https://models.example.com"
	rec := changeTestPassword(app, first)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d", rec.Code)
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if sessionTestStatus(app, cookie) != http.StatusUnauthorized {
			t.Error("previous session remains authorized")
		}
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("password change did not issue a secure replacement session")
	}
	if sessionTestStatus(app, cookies[0]) != http.StatusOK || countSessions(t, app) != 1 {
		t.Fatal("replacement session is not the only valid session")
	}
	loginCookie(t, app, "replacement-password")
}

func TestPasswordChangeRollsBackIfReplacementSessionFails(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	var before, after string
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`CREATE TRIGGER reject_session_insert BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT, 'test session failure'); END`); err != nil {
		t.Fatal(err)
	}
	rec := changeTestPassword(app, cookie)
	if rec.Code != http.StatusInternalServerError || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("failed password change status=%d", rec.Code)
	}
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || countSessions(t, app) != 1 || sessionTestStatus(app, cookie) != http.StatusOK {
		t.Fatal("failed rotation changed the password or existing session")
	}
}

func TestPasswordChangeClosesExistingEventStreams(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.AddCookie(cookie)
	done := make(chan struct{})
	go func() {
		app.Router().ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		app.eventsMu.Lock()
		subscribed := len(app.events) == 1
		app.eventsMu.Unlock()
		if subscribed {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("event stream did not subscribe")
		}
		time.Sleep(time.Millisecond)
	}
	if rec := changeTestPassword(app, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d", rec.Code)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("password change retained an old event stream")
	}
}

func TestEventStreamRechecksSessionAfterAuthorization(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	req.AddCookie(cookie)
	authorized, proceed, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	rec := httptest.NewRecorder()
	handler := app.requireDataAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(authorized)
		<-proceed
		app.handleEvents(w, r)
	}))
	go func() { handler.ServeHTTP(rec, req); close(done) }()
	<-authorized
	changed := changeTestPassword(app, cookie)
	close(proceed)
	<-done
	if changed.Code != http.StatusNoContent || rec.Code != http.StatusUnauthorized {
		t.Fatalf("change status=%d delayed stream status=%d", changed.Code, rec.Code)
	}
}

func TestPasswordChangeRejectsPreviouslyVerifiedCredentials(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	var originalHash string
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	if rec := changeTestPassword(app, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d", rec.Code)
	}
	if _, err := app.createOwnerSession(context.Background(), originalHash); !errors.Is(err, errSessionStateChanged) {
		t.Fatalf("stale login result=%v", err)
	}
	if _, err := app.rotateOwnerPassword(context.Background(), originalHash, originalHash, cookie.Value); !errors.Is(err, errSessionStateChanged) {
		t.Fatalf("stale password change result=%v", err)
	}
	if countSessions(t, app) != 1 {
		t.Fatal("stale credentials created a session")
	}
}

func TestConcurrentLoginCannotOutlivePasswordRotation(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for i := 0; i < 10; i++ {
		var originalHash string
		if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&originalHash); err != nil {
			t.Fatal(err)
		}
		replacementHash, err := hashPassword("replacement-password")
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var loginSession, replacement ownerSession
		var loginErr, rotationErr error
		var finished sync.WaitGroup
		finished.Add(2)
		go func() {
			defer finished.Done()
			<-start
			loginSession, loginErr = app.createOwnerSession(context.Background(), originalHash)
		}()
		go func() {
			defer finished.Done()
			<-start
			replacement, rotationErr = app.rotateOwnerPassword(context.Background(), originalHash, replacementHash, cookie.Value)
		}()
		close(start)
		finished.Wait()
		if rotationErr != nil {
			t.Fatal(rotationErr)
		}
		if loginErr != nil && !errors.Is(loginErr, errSessionStateChanged) {
			t.Fatal(loginErr)
		}
		if loginErr == nil && sessionTestStatus(app, &http.Cookie{Name: sessionCookieName, Value: loginSession.token}) != http.StatusUnauthorized {
			t.Fatal("concurrent login retained access after password rotation")
		}
		cookie = &http.Cookie{Name: sessionCookieName, Value: replacement.token}
		if countSessions(t, app) != 1 || sessionTestStatus(app, cookie) != http.StatusOK {
			t.Fatal("concurrent rotation did not leave only its replacement session")
		}
	}
}

func TestPasswordRotationRequiresAnUnrevokedCurrentSession(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	var originalHash string
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d", rec.Code)
	}
	if _, err := app.rotateOwnerPassword(context.Background(), originalHash, originalHash, cookie.Value); !errors.Is(err, errSessionStateChanged) {
		t.Fatalf("revoked session rotation result=%v", err)
	}
}

func TestExpiredSessionsArePrunedWithoutTheirCookies(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	now := time.Now()
	if _, err := app.db.Exec(`INSERT INTO sessions(token, expires_at) VALUES('expired-test-session', ?)`, now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	app.lastSessionCleanup.Store(now.Add(-time.Hour).Unix())
	if sessionTestStatus(app, cookie) != http.StatusOK || countSessions(t, app) != 1 {
		t.Fatal("authenticated traffic did not prune unrelated expired sessions")
	}
	if _, err := app.db.Exec(`INSERT INTO sessions(token, expires_at) VALUES('startup-expired-test-session', ?)`, now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(app.cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if countSessions(t, reopened) != 1 || sessionTestStatus(reopened, cookie) != http.StatusOK {
		t.Fatal("startup did not prune expired sessions while preserving valid ones")
	}
}

func changeTestPassword(app *App, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := jsonReq(http.MethodPost, "/api/auth/password", `{"currentPassword":"password-password","newPassword":"replacement-password"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	return rec
}

func sessionTestStatus(app *App, cookie *http.Cookie) int {
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	return rec.Code
}
