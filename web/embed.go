// Package web embeds the entire UI in infra-hub; no frontend toolchain is needed.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed index.html app.js style.css apple.svg tux.svg
var assets embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(assets, ".")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/compare/") || strings.HasPrefix(r.URL.Path, "/host/") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			page, _ := assets.ReadFile("index.html")
			w.Write(page)
			return
		}
		files.ServeHTTP(w, r)
	})
}
