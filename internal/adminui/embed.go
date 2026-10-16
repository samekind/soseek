// Package adminui serves the bundled management UI from the server binary.
package adminui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var files embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(files, "dist")
	server := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		path := strings.TrimPrefix(r.URL.Path, "/admin/")
		if path == "" {
			if _, err := fs.Stat(root, "index.html"); err != nil {
				http.Error(w, "管理面板尚未构建。请运行 scripts/build-web.sh 并重新编译服务。", http.StatusServiceUnavailable)
				return
			}
		}
		if path != "" {
			info, err := fs.Stat(root, path)
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		http.StripPrefix("/admin/", server).ServeHTTP(w, r)
	})
}
