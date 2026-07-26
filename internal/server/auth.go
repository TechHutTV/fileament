package server

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	sessionCookieName = "fileament_session"
	ownerHashKey      = "owner_password_hash"
)

type passwordRequest struct {
	Password string `json:"password"`
}

func (a *App) seedOwnerPassword() error {
	exists, err := a.ownerExists()
	if err != nil || exists || a.cfg.OwnerPassword == "" {
		return err
	}
	hash, err := hashPassword(a.cfg.OwnerPassword)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, ownerHashKey, hash)
	return err
}

func (a *App) ownerExists() (bool, error) {
	var n int
	err := a.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, ownerHashKey).Scan(&n)
	return n > 0, err
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	owner, err := a.ownerExists()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": a.validSession(r),
		"setupRequired": !owner,
	})
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	owner, err := a.ownerExists()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if owner {
		writeError(w, http.StatusConflict, errors.New("owner already configured"))
		return
	}
	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Password) < 12 {
		writeError(w, http.StatusBadRequest, errors.New("password must be at least 12 characters"))
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := a.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, ownerHashKey, hash); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var encoded string
	if err := a.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, ownerHashKey).Scan(&encoded); err != nil {
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
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	if _, err := a.db.Exec(`INSERT INTO sessions(token, expires_at) VALUES(?, ?)`, token, expires.Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secureCookies(r)})
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
	var expires int64
	if err := a.db.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, c.Value).Scan(&expires); err != nil {
		return false
	}
	if expires <= time.Now().Unix() {
		_, _ = a.db.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
		return false
	}
	return true
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
