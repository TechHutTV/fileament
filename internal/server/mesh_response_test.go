package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qmuntal/go3mf"
)

func TestMeshResponsesNeverRenderUploadedHTML(t *testing.T) {
	const prefix = "<!DOCTYPE html><script>document.title='mesh-test'</script>\n"
	binarySTL := make([]byte, 134)
	copy(binarySTL, prefix)
	binary.LittleEndian.PutUint32(binarySTL[80:], 1)
	var archive bytes.Buffer
	model := &go3mf.Model{
		Resources: go3mf.Resources{Objects: []*go3mf.Object{{ID: 1, Mesh: &go3mf.Mesh{
			Vertices:  []go3mf.Point3D{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}},
			Triangles: []go3mf.Triangle{go3mf.NewTriangle(0, 1, 2)},
		}}}},
		Build: go3mf.Build{Items: []*go3mf.Item{{ObjectID: 1, Transform: go3mf.Identity()}}},
	}
	if err := go3mf.NewEncoder(&archive).Encode(model); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		"polyglot.stl": binarySTL,
		"polyglot.obj": []byte(prefix + "v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"),
		"polyglot.3mf": append([]byte(prefix), archive.Bytes()...),
	} {
		t.Run(name, func(t *testing.T) {
			app := newAuthedTestApp(t)
			cookie := loginCookie(t, app, "password-password")
			body, contentType := multipartFile(t, name, contents)
			req := httptest.NewRequest(http.MethodPost, "/api/models", body)
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
			}
			var uploaded Model
			if err := json.Unmarshal(rec.Body.Bytes(), &uploaded); err != nil {
				t.Fatal(err)
			}
			req = jsonReq(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+uploaded.ID+`"}`)
			req.AddCookie(cookie)
			rec = httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create share status=%d", rec.Code)
			}
			var share ShareLink
			if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil {
				t.Fatal(err)
			}
			for _, route := range []struct {
				path              string
				owner, attachment bool
			}{
				{"/mesh/" + uploaded.ID + "/" + uploaded.Files[0].ID, true, false},
				{"/files/" + uploaded.ID + "/" + uploaded.Files[0].ID, true, true},
				{"/api/public/" + share.Token + "/mesh/" + uploaded.Files[0].ID, false, false},
				{"/api/public/" + share.Token + "/files/" + uploaded.Files[0].ID, false, true},
			} {
				req = httptest.NewRequest(http.MethodGet, route.path, nil)
				if route.owner {
					req.AddCookie(cookie)
				}
				rec = httptest.NewRecorder()
				app.Router().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), contents) {
					t.Fatalf("mesh bytes changed or unavailable: status=%d", rec.Code)
				}
				if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("unsafe mesh headers: type=%q nosniff=%q", rec.Header().Get("Content-Type"), rec.Header().Get("X-Content-Type-Options"))
				}
				if rec.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
					t.Fatal("missing asset sandbox")
				}
				if route.attachment && !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
					t.Fatal("download lost attachment disposition")
				}
				if !route.owner && rec.Header().Get("X-Robots-Tag") != "noindex" {
					t.Fatal("public asset lost noindex")
				}
			}
		})
	}
}
