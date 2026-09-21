package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"cloud-sync/internal/config"
)

// handleConfig serves the raw YAML editor: GET returns the file, PUT replaces
// it. All file I/O and validation live in internal/config.
func (w *WebServer) handleConfig(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, err := config.ReadFile(w.sup.ConfigPath())
		if err != nil {
			writeError(rw, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
			return
		}
		writeJSON(rw, http.StatusOK, map[string]string{"yaml": string(data)})
	case http.MethodPut:
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
		if !w.saveConfig(rw, r, []byte(body.YAML)) {
			return
		}
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleConfigForm serves the structured form editor: GET returns current
// values (token omitted), PUT merges submitted values into the config file.
func (w *WebServer) handleConfigForm(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, _, _, ok := w.sup.Snapshot()
		if !ok {
			writeError(rw, http.StatusServiceUnavailable, "state not initialized")
			return
		}
		writeJSON(rw, http.StatusOK, configFormResponse{
			Values:           config.Values(cfg),
			TokenSet:         cfg.OpenListToken != "",
			ConfigPath:       w.sup.ConfigPath(),
			MinFileSizeBytes: cfg.MinFileSize,
			RestartFields:    []string{"ui_listen"},
		})
	case http.MethodPut:
		var body struct {
			Values config.FormValues `json:"values"`
		}
		if err := readJSON(r, &body); err != nil {
			writeError(rw, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
			return
		}
		if err := config.MergeAndSave(w.sup.ConfigPath(), body.Values); err != nil {
			writeError(rw, configErrorCode(err), err.Error())
			return
		}
		if err := w.sup.Reload(r.Context()); err != nil {
			writeError(rw, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(rw, http.StatusOK, configPutResponse{OK: true, Status: statusPayload(r.Context(), w.sup)})
	default:
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleConfigRegenerate rewrites the config file from the effective config as
// a fully commented document (discarding prior comments/order).
func (w *WebServer) handleConfigRegenerate(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, _, _, ok := w.sup.Snapshot()
	if !ok {
		writeError(rw, http.StatusServiceUnavailable, "state not initialized")
		return
	}
	if err := config.RegenerateAndSave(w.sup.ConfigPath(), cfg); err != nil {
		writeError(rw, configErrorCode(err), err.Error())
		return
	}
	if err := w.sup.Reload(r.Context()); err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, configPutResponse{OK: true, Status: statusPayload(r.Context(), w.sup)})
}

// saveConfig validates+writes data (via config.SaveRaw) and reloads. It writes
// the response on failure and returns false.
func (w *WebServer) saveConfig(rw http.ResponseWriter, r *http.Request, data []byte) bool {
	if err := config.SaveRaw(w.sup.ConfigPath(), data); err != nil {
		writeError(rw, configErrorCode(err), err.Error())
		return false
	}
	if err := w.sup.Reload(r.Context()); err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return false
	}
	writeJSON(rw, http.StatusOK, configPutResponse{OK: true, Status: statusPayload(r.Context(), w.sup)})
	return true
}

func configErrorCode(err error) int {
	if config.IsValidationError(err) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
