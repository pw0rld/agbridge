package sandbox

import (
	"testing"
)

func TestPathAllowed(t *testing.T) {
	tests := []struct {
		path  string
		globs []string
		want  bool
	}{
		{"/home/user/projects/x/main.go", []string{"/home/user/projects/*"}, true},
		{"/home/user/projects/deep/sub/file", []string{"/home/user/projects/*"}, true},
		{"/etc/passwd", []string{"/home/user/projects/*"}, false},
		{"/home/user/projects/../etc/passwd", []string{"/home/user/projects/*"}, false},
		{"/home/user/projects", []string{"/home/user/projects/*"}, false},
		{"", []string{"/home/user/projects/*"}, false},
	}
	for _, tt := range tests {
		got := PathAllowed(tt.path, tt.globs)
		if got != tt.want {
			t.Errorf("PathAllowed(%q, %v) = %v, want %v", tt.path, tt.globs, got, tt.want)
		}
	}
}

func TestPathAllowedEmptyAllowlist(t *testing.T) {
	if PathAllowed("/anywhere", nil) {
		t.Errorf("empty allowlist must deny")
	}
}
