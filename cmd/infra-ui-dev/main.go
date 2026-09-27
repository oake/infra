// infra-ui-dev serves local UI files with a remote hub API, without hub state.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func main() {
	backend := flag.String("backend", "", "Hub API URL (required)")
	listen := flag.String("listen", "127.0.0.1:8788", "Local listening address")
	web := flag.String("web", "web", "UI directory")
	flag.Parse()
	if *backend == "" {
		log.Fatal("-backend is required")
	}
	target, err := url.Parse(*backend)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		log.Fatal("invalid backend URL")
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		if r.In.Header.Get("Origin") != "" {
			r.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
		}
	}}
	files := http.FileServer(http.Dir(*web))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Validate the browser origin before rewriting it for the remote hub.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/host/") || strings.HasPrefix(r.URL.Path, "/compare/") {
			http.ServeFile(w, r, *web+"/index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
	log.Printf("UI: http://%s — API: %s", *listen, *backend)
	log.Fatal(http.ListenAndServe(*listen, handler))
}
