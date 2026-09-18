package buildinfo

// Build metadata, injected at build time via -ldflags:
//
//	-X cloud-sync/internal/buildinfo.Version=<tag or branch>
//	-X cloud-sync/internal/buildinfo.Commit=<git sha>
//	-X cloud-sync/internal/buildinfo.BuildTime=<RFC3339 UTC>
//
// The defaults apply to plain `go build`. They surface in the startup log and
// in GET /api/status so a running container can be matched to a build.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)
