package server

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errSessionStateChanged = errors.New("authentication changed; sign in again")

type ownerSession struct {
	token   string
	expires time.Time
}

func newOwnerSession() (ownerSession, error) {
	token, err := randomToken(32)
	return ownerSession{token: token, expires: time.Now().Add(30 * 24 * time.Hour)}, err
}

func (a *App) createOwnerSession(ctx context.Context, expectedHash string) (ownerSession, error) {
	session, err := newOwnerSession()
	if err != nil {
		return ownerSession{}, err
	}
	result, err := a.db.ExecContext(ctx, `INSERT INTO sessions(token, expires_at)
SELECT ?, ? WHERE EXISTS (SELECT 1 FROM settings WHERE key = ? AND value = ?)`, session.token, session.expires.Unix(), ownerHashKey, expectedHash)
	if err != nil {
		return ownerSession{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ownerSession{}, err
	}
	if changed != 1 {
		return ownerSession{}, errSessionStateChanged
	}
	return session, nil
}

func (a *App) rotateOwnerPassword(ctx context.Context, expectedHash, replacementHash, currentToken string) (ownerSession, error) {
	session, err := newOwnerSession()
	if err != nil {
		return ownerSession{}, err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return ownerSession{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = ? AND value = ?
AND EXISTS (SELECT 1 FROM sessions WHERE token = ? AND expires_at > ?)`, replacementHash, ownerHashKey, expectedHash, currentToken, time.Now().Unix())
	if err != nil {
		return ownerSession{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ownerSession{}, err
	}
	if changed != 1 {
		return ownerSession{}, errSessionStateChanged
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return ownerSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token, expires_at) VALUES(?, ?)`, session.token, session.expires.Unix()); err != nil {
		return ownerSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return ownerSession{}, err
	}
	a.resetEventStreams()
	return session, nil
}

func (a *App) setOwnerSessionCookie(w http.ResponseWriter, r *http.Request, session ownerSession) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: session.token, Path: "/", Expires: session.expires,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secureCookies(r),
	})
}

func (a *App) pruneExpiredSessions(ctx context.Context, now time.Time) error {
	previous := a.lastSessionCleanup.Load()
	if previous != 0 && now.Unix()-previous < 3600 {
		return nil
	}
	if !a.lastSessionCleanup.CompareAndSwap(previous, now.Unix()) {
		return nil
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		a.lastSessionCleanup.CompareAndSwap(now.Unix(), previous)
		return err
	}
	return nil
}
