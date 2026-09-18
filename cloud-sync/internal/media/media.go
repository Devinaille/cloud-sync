// Package media holds the shared file-selection predicates used by the
// watcher, pipeline, cleanup and HTTP layers: which extensions count as media,
// which files are large enough, and whether a path is under an allowed prefix.
package media

import (
	"path/filepath"
	"strings"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".ts": true, ".iso": true,
}

// IsVideoExt reports whether path has one of the supported video extensions.
func IsVideoExt(path string) bool {
	return videoExts[strings.ToLower(filepath.Ext(path))]
}

// ShouldEmit reports whether path is an eligible video file larger than minSize.
func ShouldEmit(path string, size, minSize int64) bool {
	if !IsVideoExt(path) {
		return false
	}
	return size > minSize
}

// HasPrefix reports whether path is inside prefix (and not equal to it).
func HasPrefix(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(prefix), path)
	if err != nil {
		return false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

// Whitelisted reports whether path is under any of the allowed prefixes.
func Whitelisted(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if HasPrefix(path, p) {
			return true
		}
	}
	return false
}
