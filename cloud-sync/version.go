package main

// Build metadata, injected at build time via -ldflags:
//
//	-X main.version=<tag or branch>
//	-X main.commit=<git sha>
//	-X main.buildTime=<RFC3339 UTC>
//
// The defaults apply to plain `go build`. They surface in the startup log and
// in GET /api/status so a running container can be matched to a build.
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)
