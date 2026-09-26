package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepLinksServeApplication(t *testing.T) {
	for _, path := range []string{"/compare/anna-oake/nixos-config/old/new", "/host/anna-oake%2Fnixos-config%2Feule"} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `src="/app.js"`) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/missing.js", nil))
	if w.Code != 404 {
		t.Fatal("missing asset received application")
	}
}
