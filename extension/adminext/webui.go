// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed webui/*
var webUIFS embed.FS

// webUIHandler serves the embedded WebUI files.
type webUIHandler struct {
	fileServer http.Handler
}

// newWebUIHandler creates a new WebUI handler.
func newWebUIHandler() (*webUIHandler, error) {
	// Get the webui subdirectory
	subFS, err := fs.Sub(webUIFS, "webui")
	if err != nil {
		return nil, err
	}

	return &webUIHandler{
		fileServer: http.FileServer(http.FS(subFS)),
	}, nil
}

// ServeHTTP implements http.Handler.
func (h *webUIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set cache headers for static assets
	path := r.URL.Path
	if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
		w.Header().Set("Cache-Control", "public, max-age=31536000")
	} else if strings.HasSuffix(path, ".html") || path == "/" {
		w.Header().Set("Cache-Control", "no-cache")
	}

	h.fileServer.ServeHTTP(w, r)
}
