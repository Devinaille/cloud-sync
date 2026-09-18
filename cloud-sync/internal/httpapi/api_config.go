package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"cloud-sync/internal/config"
)

func (w *WebServer) handleConfig(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(w.sup.ConfigPath())
		if err != nil {
			writeError(rw, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
			return
		}
		writeJSON(rw, http.StatusOK, map[string]string{"yaml": string(data)})
	case http.MethodPut:
		w.putConfig(rw, r)
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (w *WebServer) putConfig(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		YAML string `json:"yaml"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if strings.TrimSpace(body.YAML) == "" {
		writeError(rw, http.StatusBadRequest, "yaml is empty")
		return
	}

	cfgPath := w.sup.ConfigPath()
	dir := filepath.Dir(cfgPath)
	tmp, err := os.CreateTemp(dir, "cloud-sync-*.yaml")
	if err != nil {
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("temp file: %v", err))
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(body.YAML); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("write temp: %v", err))
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("close temp: %v", err))
		return
	}

	// Validate by parsing the candidate file before replacing the live config.
	if _, err := config.Load(tmpName); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("chmod temp: %v", err))
		return
	}
	if err := os.Rename(tmpName, cfgPath); err != nil {
		os.Remove(tmpName)
		writeError(rw, http.StatusInternalServerError, fmt.Sprintf("replace config: %v", err))
		return
	}

	if err := w.sup.Reload(r.Context()); err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, configPutResponse{OK: true, Status: statusPayload(r.Context(), w.sup)})
}
