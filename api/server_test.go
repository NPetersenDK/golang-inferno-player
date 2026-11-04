package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestIndexVersionReplaced(t *testing.T) {
	webFS := fstest.MapFS{
		"index.html": {Data: []byte(`<span id="versionBadge">{{VERSION}}</span>`)},
		"app.js":     {Data: []byte(`{{VERSION}}`)},
	}
	s := NewServer(nil, nil, webFS, "v1.2.3")

	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, ">v1.2.3<") || strings.Contains(body, "{{VERSION}}") {
			t.Errorf("%s: code=%d body=%q", path, rec.Code, body)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: content type %q", path, ct)
		}
	}

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{{VERSION}}" {
		t.Errorf("other files must be served untouched: %d %q", rec.Code, rec.Body.String())
	}
}
