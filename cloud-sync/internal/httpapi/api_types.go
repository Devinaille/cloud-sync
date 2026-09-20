package httpapi

// statusResponse is the payload for GET /api/status.
type statusResponse struct {
	OK            bool         `json:"ok"`
	Error         string       `json:"error,omitempty"`
	Version       string       `json:"version"`
	Commit        string       `json:"commit"`
	BuildTime     string       `json:"build_time"`
	StartedAt     string       `json:"started_at,omitempty"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	OpenListPing  bool         `json:"openlist_ping"`
	TasksEnabled  bool         `json:"tasks_enabled"`
	TasksRunning  bool         `json:"tasks_running"`
	WatchDirs     []string     `json:"watch_dirs,omitempty"`
	Counts        statusCounts `json:"counts"`
	Config        *configView  `json:"config,omitempty"`
}

type statusCounts struct {
	Synced   int `json:"synced"`
	Failed   int `json:"failed"`
	Cleaned  int `json:"cleaned"`
	Unsynced int `json:"unsynced"`
	Syncing  int `json:"syncing"`
}

// configView is the effective config exposed by the API. openlist_token is
// deliberately absent.
type configView struct {
	OpenListURL       string `json:"openlist_url"`
	OpenListOverwrite bool   `json:"openlist_overwrite"`
	CleanupDryRun     bool   `json:"cleanup_dry_run"`
	UploadConcurrency int    `json:"upload_concurrency"`
	UIListen          string `json:"ui_listen"`
	TasksEnabled      bool   `json:"tasks_enabled"`
}

// fileItem is one row in GET /api/files.
type fileItem struct {
	Key        string `json:"key"`
	SrcPath    string `json:"src_path"`
	Size       int64  `json:"size"`
	State      string `json:"state"`
	CloudPath  string `json:"cloud_path"`
	SyncedAt   string `json:"synced_at"`
	CleanupAt  string `json:"cleanup_at"`
	RetryCount int    `json:"retry_count"`
	Error      string `json:"error"`
	// Progress is the upload percent (0-100) while State == "syncing".
	Progress float64 `json:"progress"`
	// Cloud is the pre-check's cloud-existence result for this file:
	// "exists" | "missing" | "unknown" | "" (not checked yet).
	Cloud string `json:"cloud"`
}

type filesResponse struct {
	Items    []fileItem `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

type configPutResponse struct {
	OK     bool           `json:"ok"`
	Status statusResponse `json:"status"`
}

type retryRequest struct {
	Keys  []string `json:"keys"`
	State string   `json:"state"`
	All   bool     `json:"all"`
}

type retryResult struct {
	Key   string `json:"key"`
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

type tasksRequest struct {
	Enabled bool `json:"enabled"`
}

type precheckItem struct {
	Key     string `json:"key"`
	SrcPath string `json:"src_path"`
	Size    int64  `json:"size"`
	// Cloud is the OpenList-side existence check: "exists" | "missing" | "unknown".
	Cloud string `json:"cloud"`
}

// precheckReport is a read-only dry run: which files would be uploaded if tasks
// were started, and whether each candidate already exists on the cloud. It is
// persisted to <SyncStatusDir>/precheck.json.
type precheckReport struct {
	GeneratedAt     string         `json:"generated_at"`
	WatchDirs       []string       `json:"watch_dirs"`
	MinFileSize     int64          `json:"min_file_size"`
	Scanned         int            `json:"scanned"`
	SkippedExt      int            `json:"skipped_ext"`
	TooSmall        int            `json:"too_small"`
	AlreadySynced   int            `json:"already_synced"`
	Candidates      []precheckItem `json:"candidates"`
	CandidatesTotal int            `json:"candidates_total"`
	CandidatesBytes int64          `json:"candidates_bytes"`
	// CloudChecked is false when the OpenList existence probe failed (see
	// CloudError); per-candidate Cloud is then "unknown".
	CloudChecked bool   `json:"cloud_checked"`
	CloudError   string `json:"cloud_error,omitempty"`
	CloudExists  int    `json:"cloud_exists"`
	CloudMissing int    `json:"cloud_missing"`
	CloudUnknown int    `json:"cloud_unknown"`
}

const precheckFileName = "precheck.json"
