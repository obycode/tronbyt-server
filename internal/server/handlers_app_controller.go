package server

// App controller — generic state store + controller UI hosting.
//
// Apps that ship a controller.html alongside their .star file can declare
// `serverComponent: true` in their manifest.yaml.  The server then:
//   - hosts the controller UI at `GET /app-api/{device_id}/{iname}/``
//   - exposes a JSON state store at `GET/POST /app-api/{device_id}/{iname}/state`
//   - auto-injects "server_url" into the app's config on install so the .star
//     file can reach /state without any manual URL configuration.
//
// State is stored per installation at {dataDir}/app-data/{device_id}/{iname}/state.json.
// The controller.html is a plain HTML/JS file uploaded alongside the .star; it
// fetches and replaces state via the relative path "state".

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
	"tronbyt-server/internal/data"
)

// handleAppControllerUIRedirect canonicalises the path to include a trailing
// slash so that relative URLs inside controller.html resolve correctly.
func (s *Server) handleAppControllerUIRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
}

// handleAppControllerUI serves the controller.html bundled with the app.
func (s *Server) handleAppControllerUI(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")
	iname := r.PathValue("iname")

	htmlPath, err := s.controllerHTMLPath(r.Context(), deviceID, iname)
	if err != nil || htmlPath == "" {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, htmlPath)
}

// handleAppControllerStateGet returns the stored JSON state for an installation.
// Returns an empty object when no state has been saved yet.
func (s *Server) handleAppControllerStateGet(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")
	iname := r.PathValue("iname")

	payload := json.RawMessage("{}")
	if raw, err := os.ReadFile(s.appStatePath(deviceID, iname)); err == nil {
		payload = raw
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(payload); err != nil {
		slog.Error("Failed to write state response", "error", err)
	}
}

// handleAppControllerStateSet replaces the stored JSON state for an installation.
func (s *Server) handleAppControllerStateSet(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("device_id")
	iname := r.PathValue("iname")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if !json.Valid(body) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	statePath := s.appStatePath(deviceID, iname)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		slog.Error("Failed to create app state directory", "path", statePath, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(statePath, body, 0o644); err != nil {
		slog.Error("Failed to write app state", "path", statePath, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		slog.Error("Failed to write state response", "error", err)
	}
}

// appStatePath returns the file path for an installation's persisted state.
func (s *Server) appStatePath(deviceID, iname string) string {
	return filepath.Join(s.DataDir, "app-data", deviceID, iname, "state.json")
}

// controllerHTMLPath looks up an app installation by device + iname, then
// resolves the path to its controller.html.  Returns "" (no error) when the
// app exists but has no controller.html.
func (s *Server) controllerHTMLPath(ctx context.Context, deviceID, iname string) (string, error) {
	app, err := gorm.G[data.App](s.DB).
		Where("device_id = ? AND iname = ?", deviceID, iname).
		First(ctx)
	if err != nil {
		return "", err
	}
	if app.Path == nil || *app.Path == "" {
		return "", nil
	}

	appDir := filepath.FromSlash(*app.Path)
	if strings.HasSuffix(appDir, ".star") || strings.HasSuffix(appDir, ".webp") {
		appDir = filepath.Dir(appDir)
	}

	htmlPath := filepath.Join(s.DataDir, appDir, "controller.html")
	if _, err := os.Stat(htmlPath); err != nil {
		return "", nil // no controller — not an error
	}
	return htmlPath, nil
}

// baseURL derives the server's external base URL from the incoming request,
// respecting X-Forwarded-Proto / X-Forwarded-Host set by the proxy middleware.
func baseURL(r *http.Request) string {
	scheme := r.URL.Scheme
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}

// appHasServerComponent reads the app's manifest.yaml and returns whether it
// declares serverComponent: true.  appPath is relative to dataDir and may be
// either a directory or a .star/.webp file path.
func appHasServerComponent(dataDir, appPath string) bool {
	dir := filepath.FromSlash(appPath)
	if strings.HasSuffix(dir, ".star") || strings.HasSuffix(dir, ".webp") {
		dir = filepath.Dir(dir)
	}

	manifestPath := filepath.Join(dataDir, dir, "manifest.yaml")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return false
	}

	var manifest struct {
		ServerComponent bool `yaml:"serverComponent"`
	}
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return false
	}
	return manifest.ServerComponent
}
