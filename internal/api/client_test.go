package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAndRedirectBoundary(t *testing.T) {
	requests := 0
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write([]byte(`{}`)) }))
	defer dest.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer shared-edge-token" {
			t.Error("missing configured bearer token")
		}
		http.Redirect(w, r, dest.URL, 302)
	}))
	defer server.Close()
	client, e := NewClient(server.URL, "shared-edge-token")
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Do(t.Context(), "POST", "/api/beacon", map[string]string{"host": "owner/repo/host"}, nil); e == nil {
		t.Fatal("redirect silently accepted")
	}
	if requests != 0 {
		t.Fatal("bearer followed a redirect")
	}
	if _, e = NewClient("http://public.example", ""); e == nil {
		t.Fatal("plaintext remote hub allowed")
	}
}
