// Package server exposes the JSON API and serves the embedded UI.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"entropy/internal/app"
)

func New(a *app.App, ui fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/environment", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.View(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		res := a.Sync(r.Context())
		status := http.StatusOK
		if res.Error != "" {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, res)
	})

	mux.HandleFunc("POST /api/faults/stop-all", func(w http.ResponseWriter, r *http.Request) {
		if err := a.StopAllFaults(r.Context()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// Runs the command and answers when it finishes (at most its timeoutSec).
	mux.HandleFunc("POST /api/services/{service}/commands/{command}/run", func(w http.ResponseWriter, r *http.Request) {
		run, err := a.RunCommand(r.Context(), r.PathValue("service"), r.PathValue("command"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})

	// Runs the service's health command now; answers with the new status.
	mux.HandleFunc("POST /api/services/{service}/check", func(w http.ResponseWriter, r *http.Request) {
		st, err := a.CheckService(r.Context(), r.PathValue("service"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})

	mux.HandleFunc("GET /api/command-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		run, err := a.CommandRun(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})

	mux.Handle("/", spa(ui))
	return mux
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, app.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrAlreadyRunning):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// spa serves the built UI, falling back to index.html for client-side routes.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(ui, "index.html"); err != nil {
			http.Error(w, "The UI is not built yet: run `npm install && npm run build` in web/, then restart Entropy.", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if _, err := fs.Stat(ui, name); err != nil {
				r.URL.Path = "/"
			}
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
