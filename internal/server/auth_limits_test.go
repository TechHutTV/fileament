package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthenticationRejectsOversizedInput(t *testing.T) {
	for _, route := range []string{"setup", "login", "password"} {
		for _, oversizedBody := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/body=%t", route, oversizedBody), func(t *testing.T) {
				app := newAuthedTestApp(t)
				var cookie *http.Cookie
				if route == "setup" {
					app = newTestApp(t)
				} else if route == "password" {
					cookie = loginCookie(t, app, "password-password")
				}
				size, want := 1025, http.StatusBadRequest
				if oversizedBody {
					size, want = 20<<10, http.StatusRequestEntityTooLarge
				}
				payload := map[string]string{"password": strings.Repeat("x", size)}
				if route == "password" {
					payload = map[string]string{"currentPassword": "password-password", "newPassword": strings.Repeat("x", size)}
				}
				body, _ := json.Marshal(payload)
				req := jsonReq(http.MethodPost, "/api/auth/"+route, string(body))
				req.ContentLength = -1
				if cookie != nil {
					req.AddCookie(cookie)
				}
				rec := httptest.NewRecorder()
				app.Router().ServeHTTP(rec, req)
				if rec.Code != want {
					t.Fatalf("status=%d want=%d", rec.Code, want)
				}
				var failure map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil || failure["error"] == "" {
					t.Fatal("missing JSON error response")
				}
			})
		}
	}
}

func TestAuthenticationLimitsAttemptsAcrossForwardedAddresses(t *testing.T) {
	app := newAuthedTestApp(t)
	frozen := time.Now()
	app.authLimits.now = func() time.Time { return frozen }
	for i := 0; i < 11; i++ {
		req := jsonReq(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`)
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i+1))
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if i == 10 {
			want = http.StatusTooManyRequests
			if rec.Header().Get("Retry-After") == "" {
				t.Error("rate limit did not include Retry-After")
			}
		}
		if rec.Code != want {
			t.Fatalf("attempt %d status=%d want=%d", i+1, rec.Code, want)
		}
	}
}

func TestAuthenticationLimiterReplenishesWithoutAccumulatingExtraBurst(t *testing.T) {
	var limiter authenticationLimiter
	now := time.Now()
	consumeBurst := func(at time.Time) {
		t.Helper()
		for i := 0; i < 10; i++ {
			if retry := limiter.acquire(at); retry != 0 {
				t.Fatalf("attempt %d rejected with retry=%v", i+1, retry)
			}
			limiter.release()
		}
		if retry := limiter.acquire(at); retry != 6*time.Second {
			t.Fatalf("exhausted burst retry=%v", retry)
		}
	}
	consumeBurst(now)
	if retry := limiter.acquire(now.Add(5 * time.Second)); retry != time.Second {
		t.Fatalf("partial refill retry=%v", retry)
	}
	if retry := limiter.acquire(now.Add(6 * time.Second)); retry != 0 {
		t.Fatalf("refilled attempt rejected with retry=%v", retry)
	}
	limiter.release()
	consumeBurst(now.Add(time.Hour))
}

func TestAuthenticationRequiresOneJSONValue(t *testing.T) {
	for _, test := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"plain text", "text/plain", `{"password":"password-password"}`, http.StatusUnsupportedMediaType},
		{"trailing value", "application/json", `{"password":"password-password"} {}`, http.StatusBadRequest},
		{"oversized whitespace", "application/json", `{"password":"password-password"}` + strings.Repeat(" ", 20<<10), http.StatusRequestEntityTooLarge},
		{"JSON charset", "application/json; charset=utf-8", `{"password":"password-password"}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := newAuthedTestApp(t)
			req := jsonReq(http.MethodPost, "/api/auth/login", test.body)
			req.ContentLength = -1
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d want=%d", rec.Code, test.status)
			}
		})
	}
}

func TestAuthenticationBoundsConcurrentRequests(t *testing.T) {
	app := newAuthedTestApp(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		body := &blockedAuthBody{ready: make(chan struct{}), release: release, contents: strings.NewReader(`{"password":"wrong"}`)}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		go func() {
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			results <- rec.Code
		}()
		select {
		case <-body.ready:
		case <-time.After(time.Second):
			t.Fatal("authentication did not start reading its body")
		}
	}
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("third concurrent authentication status=%d", rec.Code)
	}
	unblock()
	for i := 0; i < 2; i++ {
		select {
		case status := <-results:
			if status != http.StatusUnauthorized {
				t.Errorf("admitted authentication status=%d", status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("admitted authentication did not finish")
		}
	}
}

func TestAuthenticationTimesOutStalledBody(t *testing.T) {
	app := newAuthedTestApp(t)
	server := httptest.NewServer(app.Router())
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(7 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "POST /api/auth/login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("stalled body status=%d", response.StatusCode)
	}
	locked := make(chan struct{})
	go func() {
		app.dataMu.Lock()
		app.dataMu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("stalled authentication retained the maintenance lock")
	}
}

type blockedAuthBody struct {
	ready    chan struct{}
	release  <-chan struct{}
	once     sync.Once
	contents *strings.Reader
}

func (b *blockedAuthBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.ready) })
	<-b.release
	return b.contents.Read(p)
}
