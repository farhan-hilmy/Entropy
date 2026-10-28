// Package server exposes the JSON API and serves the embedded UI.
package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"entropy/internal/app"
	"entropy/internal/config"
	"entropy/internal/faults"
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

	// The config file as written, plus the hash to send back with an edit.
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		snap, err := a.Config()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	// Saves the editable fields of one part of the config, then syncs.
	// Commands can't be edited here, only in the file.
	mux.HandleFunc("PATCH /api/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Hash string `json:"hash"`
			config.Edit
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res, err := a.EditConfig(r.Context(), body.Hash, body.Edit)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// Applies a fault (faults.Spec) to the dependency; it's removed after durationSec.
	mux.HandleFunc("POST /api/dependencies/{name}/fault", func(w http.ResponseWriter, r *http.Request) {
		var spec faults.Spec
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		f, err := a.StartFault(r.Context(), r.PathValue("name"), spec)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	})

	mux.HandleFunc("DELETE /api/dependencies/{name}/fault", func(w http.ResponseWriter, r *http.Request) {
		if err := a.StopFault(r.PathValue("name")); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	// Runs one of the service's requests ({service, command}; command empty =
	// its health check) and reports whether it went through the dependency's
	// proxy. Answers when the request finishes.
	mux.HandleFunc("POST /api/dependencies/{name}/verify", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Service string `json:"service"`
			Command string `json:"command"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		rc, err := a.VerifyRoute(r.Context(), r.PathValue("name"), body.Service, body.Command)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rc)
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
	var invalid *config.InvalidError
	switch {
	case errors.Is(err, app.ErrNotFound), errors.Is(err, config.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrAlreadyRunning), errors.Is(err, config.ErrConflict), errors.Is(err, faults.ErrBusy),
		errors.Is(err, app.ErrVerifying), errors.Is(err, app.ErrFaultActive):
		status = http.StatusConflict
	case errors.As(err, &invalid), errors.Is(err, faults.ErrInvalid), errors.Is(err, app.ErrNoTrigger):
		status = http.StatusUnprocessableEntity
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
