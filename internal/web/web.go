// Package web serves the embedded static UI assets.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assetsFS embed.FS

// Assets returns an http.Handler serving the embedded UI at /.
func Assets() http.Handler {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
