// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed webui/*
var webUIFS embed.FS

// webUIHandler serves the embedded WebUI files.
type webUIHandler struct {
	fsys fs.FS
}

// newWebUIHandler creates a new WebUI handler.
func newWebUIHandler() (*webUIHandler, error) {
	// Get the webui subdirectory
	subFS, err := fs.Sub(webUIFS, "webui")
	if err != nil {
		return nil, err
	}

	return &webUIHandler{
		fsys: subFS,
	}, nil
}

// ServeHTTP implements http.Handler.
func (h *webUIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean the path
	urlPath := r.URL.Path
	if urlPath == "" || urlPath == "/" {
		urlPath = "/index.html"
	}
	
	// Remove leading slash for fs.FS
	filePath := strings.TrimPrefix(urlPath, "/")
	
	// Try to open the file
	file, err := h.fsys.Open(filePath)
	if err != nil {
		// File not found, serve index.html for SPA routing
		filePath = "index.html"
		file, err = h.fsys.Open(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer file.Close()
	
	// Check if it's a directory
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if stat.IsDir() {
		// Serve index.html from directory
		file.Close()
		filePath = path.Join(filePath, "index.html")
		file, err = h.fsys.Open(filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		stat, _ = file.Stat()
	}
	
	// Set content type based on extension
	ext := path.Ext(filePath)
	switch ext {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	case ".json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	case ".woff2":
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	case ".woff":
		w.Header().Set("Content-Type", "font/woff")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	case ".ttf":
		w.Header().Set("Content-Type", "font/ttf")
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	}
	
	// Serve the file
	http.ServeContent(w, r, filePath, stat.ModTime(), file.(readSeeker))
}

// readSeeker combines io.Reader and io.Seeker
type readSeeker interface {
	Read(p []byte) (n int, err error)
	Seek(offset int64, whence int) (int64, error)
}
