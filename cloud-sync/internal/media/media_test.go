package media

import "testing"

func TestShouldEmit_FilterByExtAndSize(t *testing.T) {
	cases := []struct {
		path string
		size int64
		want bool
	}{
		{"/m/a.mkv", 200 * 1024 * 1024, true},
		{"/m/a.mp4", 200 * 1024 * 1024, true},
		{"/m/a.ts", 200 * 1024 * 1024, true},
		{"/m/a.iso", 200 * 1024 * 1024, true},
		{"/m/a.txt", 200 * 1024 * 1024, false},
		{"/m/a.jpg", 200 * 1024 * 1024, false},
		{"/m/a.nfo", 200 * 1024 * 1024, false},
		{"/m/a.mkv", 99 * 1024 * 1024, false},
		{"/m/a.mkv", 100 * 1024 * 1024, false},
		{"/m/a.mkv", 100*1024*1024 + 1, true},
	}
	for _, tc := range cases {
		if got := ShouldEmit(tc.path, tc.size, 100*1024*1024); got != tc.want {
			t.Errorf("ShouldEmit(%q, %d) = %v, want %v", tc.path, tc.size, got, tc.want)
		}
	}
}

func TestHasPrefix(t *testing.T) {
	cases := []struct {
		path, prefix string
		want         bool
	}{
		{"/a/b/c.mkv", "/a", true},
		{"/a/b/c.mkv", "/a/b", true},
		{"/a/b/c.mkv", "/a/b/c.mkv", false},
		{"/a/b/c.mkv", "/a/x", false},
		{"/a/b/c.mkv", "", false},
		{"/ab/c.mkv", "/a", false},
	}
	for _, tc := range cases {
		if got := HasPrefix(tc.path, tc.prefix); got != tc.want {
			t.Errorf("HasPrefix(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestWhitelisted(t *testing.T) {
	if !Whitelisted("/a/b/c.mkv", []string{"/x", "/a"}) {
		t.Error("Whitelisted under second prefix = false, want true")
	}
	if Whitelisted("/a/b/c.mkv", []string{"/x", "/y"}) {
		t.Error("Whitelisted under no prefix = true, want false")
	}
}
