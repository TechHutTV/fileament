package server

import (
	"errors"
	"mime"
	"net/http"
)

func protectBrowserOrigin(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rejectRequest(w, r, http.StatusForbidden, "cross-origin mutations are not allowed")
	}))
	return protection.Handler(next)
}

func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			rejectRequest(w, r, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func rejectRequest(w http.ResponseWriter, r *http.Request, status int, message string) {
	if r.ProtoMajor == 1 {
		// Reject without draining an attacker-controlled request body.
		w.Header().Set("Connection", "close")
	}
	writeError(w, status, errors.New(message))
}
