package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestEventStreamFlushesBeforeAnyThumbnail(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	srv := httptest.NewServer(app.Router())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("idle stream did not open promptly: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Cache-Control") != "private, no-store" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream status=%d headers=%v", res.StatusCode, res.Header)
	}
	reader := bufio.NewReader(res.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("initial comment=%q error=%v", line, err)
	}
	line, err = reader.ReadString('\n')
	if err != nil || line != "\n" {
		t.Fatalf("initial frame terminator=%q error=%v", line, err)
	}
	app.publishEvent(ThumbnailEvent{ModelID: "model", FileID: "file", Status: "done"})
	line, err = reader.ReadString('\n')
	if err != nil || line != "event: thumbnail\n" {
		t.Fatalf("thumbnail event=%q error=%v", line, err)
	}
}

func TestEventStreamRejectsRequestBodyWithoutWaiting(t *testing.T) {
	for _, framing := range []string{"Content-Length: 1", "Transfer-Encoding: chunked"} {
		t.Run(framing, func(t *testing.T) {
			app := newAuthedTestApp(t)
			req := eventTestRequest(t, app)
			srv := httptest.NewServer(app.Router())
			defer srv.Close()
			conn, err := net.DialTimeout("tcp", strings.TrimPrefix(srv.URL, "http://"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintf(conn, "GET /api/events HTTP/1.1\r\nHost: fileament.test\r\nCookie: %s\r\n%s\r\n\r\n", req.Header.Get("Cookie"), framing); err != nil {
				t.Fatal(err)
			}
			res, err := http.ReadResponse(bufio.NewReader(conn), req)
			if err != nil {
				t.Fatalf("event request waited for an unused body: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusBadRequest || res.Header.Get("Content-Type") != "application/json" || !res.Close {
				t.Fatalf("body rejection status=%d contentType=%q close=%v", res.StatusCode, res.Header.Get("Content-Type"), res.Close)
			}
			if _, err := io.Copy(io.Discard, res.Body); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("rejected body retained the connection: %v", err)
			}
		})
	}
}

func TestEventStreamHeartbeatsAndLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := newAuthedTestApp(t)
		defer app.Close()
		req := eventTestRequest(t, app)
		w := &eventTestWriter{ResponseRecorder: httptest.NewRecorder()}
		done := make(chan struct{})
		go func() { app.Router().ServeHTTP(w, req); close(done) }()
		synctest.Wait()
		state := w.snapshot()
		if state.flushes != 1 || state.body != ": connected\n\n" {
			t.Fatalf("initial flushes=%d body=%q", state.flushes, state.body)
		}
		time.Sleep(15 * time.Second)
		synctest.Wait()
		state = w.snapshot()
		if state.flushes != 2 || state.body != ": connected\n\n: heartbeat\n\n" {
			t.Fatalf("heartbeat flushes=%d body=%q", state.flushes, state.body)
		}
		app.publishEvent(ThumbnailEvent{ModelID: "model", Status: "done"})
		synctest.Wait()
		state = w.snapshot()
		if state.flushes != 3 || !strings.Contains(state.body, "event: thumbnail\ndata: {\"modelId\":\"model\"") {
			t.Fatal("thumbnail was not flushed after heartbeat")
		}
		if len(state.deadlines) != 6 || !state.deadlines[len(state.deadlines)-1].IsZero() {
			t.Fatal("write deadline was not cleared between frames")
		}
		for i := 0; i < len(state.deadlines); i += 2 {
			if remaining := state.deadlines[i].Sub(state.writeTimes[i/2]); remaining <= 0 || remaining > 10*time.Second {
				t.Fatalf("write time limit=%v", remaining)
			}
		}
		time.Sleep(30*time.Minute - 16*time.Second)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("valid stream ended before its lifetime limit")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		assertEventStreamClosed(t, app, done)
	})
}

func TestEventStreamDisconnectsAndRevalidates(t *testing.T) {
	for _, reason := range []string{"cancel", "reset", "close", "idle expiry", "expiry before event", "maintenance"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				app := newAuthedTestApp(t)
				defer app.Close()
				req := eventTestRequest(t, app)
				ctx, cancel := context.WithCancel(req.Context())
				defer cancel()
				w := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { app.Router().ServeHTTP(w, req.WithContext(ctx)); close(done) }()
				synctest.Wait()
				switch reason {
				case "cancel":
					cancel()
				case "reset":
					app.resetEventStreams()
				case "close":
					if err := app.Close(); err != nil {
						t.Fatal(err)
					}
				case "idle expiry", "expiry before event":
					if _, err := app.db.Exec(`UPDATE sessions SET expires_at=?`, time.Now().Unix()); err != nil {
						t.Fatal(err)
					}
					if reason == "idle expiry" {
						time.Sleep(time.Minute)
					} else {
						app.publishEvent(ThumbnailEvent{ModelID: "must-not-send", Status: "done"})
					}
				case "maintenance":
					app.maintenance.Store(true)
					app.publishEvent(ThumbnailEvent{ModelID: "must-not-send", Status: "done"})
				}
				synctest.Wait()
				assertEventStreamClosed(t, app, done)
				if strings.Contains(w.Body.String(), "event: thumbnail") {
					t.Fatal("stream sent data after its authorization changed")
				}
			})
		})
	}
}

func TestEventStreamStopsOnWriteOrFlushFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		write, flush int
		deadlineErr  error
	}{
		{name: "initial write", write: 1},
		{name: "initial flush", flush: 1},
		{name: "event write", write: 2},
		{name: "event flush", flush: 2},
		{name: "unsupported flush", flush: -1},
		{name: "deadline failure", deadlineErr: io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				app := newAuthedTestApp(t)
				defer app.Close()
				w := &eventTestWriter{ResponseRecorder: httptest.NewRecorder(), failWrite: tc.write, failFlush: tc.flush, deadlineErr: tc.deadlineErr}
				done := make(chan struct{})
				req := eventTestRequest(t, app)
				go func() { app.Router().ServeHTTP(eventWriterWrapper{w}, req); close(done) }()
				synctest.Wait()
				if tc.write == 2 || tc.flush == 2 {
					app.publishEvent(ThumbnailEvent{ModelID: "model", Status: "done"})
					synctest.Wait()
				}
				assertEventStreamClosed(t, app, done)
			})
		})
	}
}

func TestEventStreamTimesOutAStalledConnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := newAuthedTestApp(t)
		defer app.Close()
		client, server := net.Pipe()
		defer client.Close()
		listener := &eventPipeListener{connections: make(chan net.Conn, 1), done: make(chan struct{}), addr: server.LocalAddr()}
		listener.connections <- server
		srv := &http.Server{Handler: app.Router()}
		defer srv.Close()
		go func() {
			if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("serve: %v", err)
			}
		}()
		req := eventTestRequest(t, app)
		req.URL.Host = "fileament.test"
		if err := req.Write(client); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		app.eventsMu.Lock()
		subscribers := len(app.events)
		app.eventsMu.Unlock()
		if subscribers != 1 {
			t.Fatalf("stalled connection subscribers=%d", subscribers)
		}
		// The client deliberately never reads the response from the real HTTP server.
		time.Sleep(10 * time.Second)
		synctest.Wait()
		app.eventsMu.Lock()
		subscribers = len(app.events)
		app.eventsMu.Unlock()
		if subscribers != 0 {
			t.Fatal("stalled connection retained its event subscription")
		}
	})
}

func eventTestRequest(t *testing.T, app *App) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.AddCookie(loginCookie(t, app, "password-password"))
	return req
}

func assertEventStreamClosed(t *testing.T, app *App, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	default:
		t.Fatal("event stream is still open")
	}
	app.eventsMu.Lock()
	defer app.eventsMu.Unlock()
	if len(app.events) != 0 {
		t.Fatal("event stream left a subscription behind")
	}
}

type eventTestWriter struct {
	*httptest.ResponseRecorder
	mu                                    sync.Mutex
	writes, flushes, failWrite, failFlush int
	deadlines, writeTimes                 []time.Time
	deadlineErr                           error
}

func (w *eventTestWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	w.writeTimes = append(w.writeTimes, time.Now())
	if w.writes == w.failWrite {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func (w *eventTestWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *eventTestWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
	if w.failFlush == -1 {
		return http.ErrNotSupported
	}
	if w.flushes == w.failFlush {
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	return nil
}

func (w *eventTestWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines = append(w.deadlines, deadline)
	return w.deadlineErr
}

type eventWriterSnapshot struct {
	body                  string
	flushes               int
	deadlines, writeTimes []time.Time
}

func (w *eventTestWriter) snapshot() eventWriterSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return eventWriterSnapshot{w.Body.String(), w.flushes, append([]time.Time(nil), w.deadlines...), append([]time.Time(nil), w.writeTimes...)}
}

type eventWriterWrapper struct{ http.ResponseWriter }

func (w eventWriterWrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type eventPipeListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
	addr        net.Addr
}

func (l *eventPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *eventPipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *eventPipeListener) Addr() net.Addr { return l.addr }
