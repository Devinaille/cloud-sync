package httpapi

import (
	"net/http"
	"sort"
	"strings"
)

func (w *WebServer) handleFiles(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, st, _, ok := w.sup.Snapshot()
	if !ok {
		writeError(rw, http.StatusServiceUnavailable, "supervisor not running")
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "all"
	}
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), defaultPageSize)
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	records, err := listRecords(st)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	items, err := buildFileItems(cfg, st, records)
	if err != nil {
		writeError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	applyCloudStatus(cfg, items)

	filtered := filterItems(items, state, q.Get("q"))
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Key < filtered[j].Key })

	resp := filesResponse{
		Items:    paginate(filtered, page, pageSize),
		Total:    len(filtered),
		Page:     page,
		PageSize: pageSize,
	}
	writeJSON(rw, http.StatusOK, resp)
}

func filterItems(items []fileItem, state, query string) []fileItem {
	out := make([]fileItem, 0, len(items))
	q := strings.ToLower(strings.TrimSpace(query))
	for _, item := range items {
		if state != "all" && item.State != state {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(item.Key), q) &&
			!strings.Contains(strings.ToLower(item.SrcPath), q) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func paginate(items []fileItem, page, pageSize int) []fileItem {
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []fileItem{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
