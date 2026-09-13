package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookieName = "fileament_session"
	ownerHashKey      = "owner_password_hash"
)

type passwordRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (a *App) seedOwnerPassword() error {
	exists, err := a.ownerExists()
	if err != nil || exists || a.cfg.OwnerPassword == "" {
		return err
	}
	if err := validateNewPassword(a.cfg.OwnerPassword); err != nil {
		return fmt.Errorf("invalid FILEAMENT_OWNER_PASSWORD: %w", err)
	}
	hash, err := hashPassword(a.cfg.OwnerPassword)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, ownerHashKey, hash)
	return err
}

func (a *App) ownerExists() (bool, error) {
	return a.ownerExistsContext(context.Background())
}

func (a *App) ownerExistsContext(ctx context.Context) (bool, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = ?`, ownerHashKey).Scan(&n)
	return n > 0, err
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	owner, err := a.ownerExistsContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	authenticated := a.validSession(r)
	response := map[string]any{
		"authenticated": authenticated,
		"setupRequired": !owner,
	}
	if authenticated {
		cookie, _ := r.Cookie(sessionCookieName)
		response["context"] = ownerSessionContext(cookie.Value)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	owner, err := a.ownerExistsContext(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if owner {
		writeError(w, http.StatusConflict, errors.New("owner already configured"))
		return
	}
	var req passwordRequest
	if !decodeAuthJSON(w, r, &req) {
		return
	}
	if err := validateNewPassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO settings(key, value) VALUES(?, ?)`, ownerHashKey, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !decodeAuthJSON(w, r, &req) || !authPasswordSizeAllowed(w, req.Password) {
		return
	}
	if err := a.pruneExpiredSessions(r.Context(), time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var encoded string
	if err := a.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusConflict, errors.New("owner setup required"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ok, err := verifyPassword(req.Password, encoded)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, errors.New("invalid password"))
		return
	}
	session, err := a.createOwnerSession(r.Context(), encoded)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errSessionStateChanged) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err)
		return
	}
	a.setOwnerSessionCookie(w, r, session)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		result, err := a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token = ?`, c.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		changed, err := result.RowsAffected()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if changed > 0 {
			a.resetEventStreams()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secureCookies(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if !decodeAuthJSON(w, r, &req) || !authPasswordSizeAllowed(w, req.CurrentPassword) {
		return
	}
	if err := validateNewPassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var encoded string
	if err := a.db.QueryRowContext(r.Context(), `SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&encoded); err != nil {
		writeError(w, http.StatusConflict, errors.New("owner setup required"))
		return
	}
	ok, err := verifyPassword(req.CurrentPassword, encoded)
	if err != nil || !ok {
		writeError(w, http.StatusUnauthorized, errors.New("invalid current password"))
		return
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, errSessionStateChanged)
		return
	}
	session, err := a.rotateOwnerPassword(r.Context(), encoded, hash, cookie.Value)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errSessionStateChanged) {
			status = http.StatusUnauthorized
		}
		writeError(w, status, err)
		return
	}
	a.setOwnerSessionCookie(w, r, session)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) secureCookies(r *http.Request) bool {
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	u, err := url.Parse(a.cfg.BaseURL)
	return err == nil && strings.EqualFold(u.Scheme, "https")
}

func (a *App) validSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	if a.db == nil || a.pruneExpiredSessions(r.Context(), time.Now()) != nil {
		return false
	}
	var expires int64
	if err := a.db.QueryRowContext(r.Context(), `SELECT expires_at FROM sessions WHERE token = ?`, c.Value).Scan(&expires); err != nil {
		return false
	}
	if expires <= time.Now().Unix() {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token = ?`, c.Value)
		return false
	}
	expected := r.Header.Get("X-Fileament-Context")
	return expected == "" || expected == ownerSessionContext(c.Value)
}

func ownerSessionContext(token string) string {
	context := sha256.Sum256([]byte("fileament:owner-context:" + token))
	return fmt.Sprintf("%x", context)
}

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.validSession(r) {
			writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) requireDataAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.dataMu.RLock()
		valid := a.validSession(r)
		a.dataMu.RUnlock()
		if !valid {
			writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateNewPassword(password string) error {
	if err := validatePasswordSize(password); err != nil {
		return err
	}
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 {
		return errors.New("password must be at least 12 Unicode characters")
	}
	return nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false, errors.New("invalid hash")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
