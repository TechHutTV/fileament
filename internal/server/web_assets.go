package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hashedWebAsset = regexp.MustCompile(`^assets/[^/]+-[A-Za-z0-9_-]{8}\.(js|css)$`)
var encodingWeight = regexp.MustCompile(`^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$`)

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if strings.HasPrefix(r.URL.Path, "/s/") {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	info, err := fs.Stat(a.webFS, name)
	if err != nil || !info.Mode().IsRegular() {
		if name == "assets" || strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(a.webFS, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
		return
	}
	if hashedWebAsset.MatchString(name) {
		w.Header().Add("Vary", "Accept-Encoding")
		compressed, err := fs.Stat(a.webFS, name+".gz")
		hasGzip := err == nil && compressed.Mode().IsRegular()
		encoding, acceptable := webEncoding(strings.Join(r.Header.Values("Accept-Encoding"), ","), hasGzip)
		if !acceptable {
			http.Error(w, "No acceptable asset encoding", http.StatusNotAcceptable)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
		// Build hashes identify content; encodings have separate weak validators.
		w.Header().Set("ETag", fmt.Sprintf(`W/"%x"`, sha256.Sum256([]byte(name+":"+encoding))))
		if encoding == "gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.FormatInt(compressed.Size(), 10))
			r = r.Clone(r.Context())
			r.URL.Path = "/" + name + ".gz"
			r.URL.RawPath = ""
			// Serve gzip whole; identity responses retain byte ranges.
			r.Header.Del("Range")
		}
	}
	http.FileServer(http.FS(a.webFS)).ServeHTTP(w, r)
}

func webEncoding(header string, hasGzip bool) (string, bool) {
	weights := make(map[string]float64)
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(entry, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name == "x-gzip" {
			name = "gzip"
		}
		if name != "gzip" && name != "identity" && name != "*" {
			continue
		}
		q := 1.0
		if len(parts) > 1 {
			key, value, found := strings.Cut(strings.TrimSpace(parts[1]), "=")
			value = strings.TrimSpace(value)
			if len(parts) != 2 || !found || !strings.EqualFold(strings.TrimSpace(key), "q") || !encodingWeight.MatchString(value) {
				q = 0
			} else {
				q, _ = strconv.ParseFloat(value, 64)
			}
		}
		if previous, exists := weights[name]; exists && previous < q {
			q = previous
		}
		weights[name] = q
	}
	gzipWeight, explicit := weights["gzip"]
	if !explicit {
		gzipWeight = weights["*"]
	}
	if !hasGzip {
		gzipWeight = 0
	}
	identityWeight, explicitIdentity := weights["identity"]
	if !explicitIdentity {
		identityWeight = 1
		if wildcard, exists := weights["*"]; exists && wildcard == 0 {
			identityWeight = 0
		}
	}
	if gzipWeight > 0 && (!explicitIdentity || gzipWeight >= identityWeight) {
		return "gzip", true
	}
	return "", identityWeight > 0
}
