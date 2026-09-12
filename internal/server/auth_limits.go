package server

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	maxAuthBodyBytes  = 16 << 10
	maxPasswordBytes  = 1024
	authReadTimeout   = 5 * time.Second
	authAttemptBurst  = 10
	authRefillPeriod  = 6 * time.Second
	maxConcurrentAuth = 2
)

type authenticationLimiter struct {
	mu     sync.Mutex
	active int
	tokens float64
	last   time.Time
}

func (l *authenticationLimiter) acquire(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.tokens = authAttemptBurst
	} else {
		l.tokens = math.Min(authAttemptBurst, l.tokens+now.Sub(l.last).Seconds()/authRefillPeriod.Seconds())
	}
	l.last = now
	if l.tokens < 1 {
		return time.Duration(math.Ceil((1-l.tokens)*authRefillPeriod.Seconds())) * time.Second
	}
	if l.active >= maxConcurrentAuth {
		return time.Second
	}
	l.tokens--
	l.active++
	return 0
}

func (l *authenticationLimiter) release() {
	l.mu.Lock()
	l.active--
	l.mu.Unlock()
}

func (a *App) authenticationLimitsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/api/auth/setup" && r.URL.Path != "/api/auth/login" && r.URL.Path != "/api/auth/password") {
			next.ServeHTTP(w, r)
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeAuthInputError(w, r, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
			return
		}
		if r.ContentLength > maxAuthBodyBytes {
			writeAuthInputError(w, r, http.StatusRequestEntityTooLarge, errors.New("authentication request is too large"))
			return
		}
		if retry := a.authLimits.acquire(time.Now()); retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
			writeAuthInputError(w, r, http.StatusTooManyRequests, errors.New("too many authentication attempts; retry later"))
			return
		}
		defer a.authLimits.release()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(authReadTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			writeAuthInputError(w, r, http.StatusInternalServerError, errors.New("cannot set authentication deadline"))
			return
		}
		defer controller.SetReadDeadline(time.Time{})
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(value)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	var networkError net.Error
	switch {
	case errors.As(err, &tooLarge):
		writeAuthInputError(w, r, http.StatusRequestEntityTooLarge, errors.New("authentication request is too large"))
	case errors.As(err, &networkError) && networkError.Timeout():
		writeAuthInputError(w, r, http.StatusRequestTimeout, errors.New("authentication request timed out"))
	default:
		writeAuthInputError(w, r, http.StatusBadRequest, errors.New("invalid authentication JSON"))
	}
	return false
}

func authPasswordSizeAllowed(w http.ResponseWriter, password string) bool {
	if err := validatePasswordSize(password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func validatePasswordSize(password string) error {
	if len(password) > maxPasswordBytes {
		return errors.New("password must not exceed 1024 bytes")
	}
	return nil
}

func writeAuthInputError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if r.ProtoMajor == 1 {
		// Do not drain an incomplete body after rejecting authentication.
		w.Header().Set("Connection", "close")
	}
	writeError(w, status, err)
}
