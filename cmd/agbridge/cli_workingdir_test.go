package main

import "testing"

func TestResolveRemotePath(t *testing.T) {
	cwd := "/home/ubuntu"
	cases := []struct{ arg, want string }{
		{"", "/home/ubuntu"},
		{"src", "/home/ubuntu/src"},
		{"src/main.go", "/home/ubuntu/src/main.go"},
		{"./a/./b", "/home/ubuntu/a/b"},
		{"../etc/hosts", "/home/etc/hosts"}, // .. allowed, no fence
		{"/etc/passwd", "/etc/passwd"},      // absolute passthrough
		{"/a/../b", "/b"},
	}
	for _, c := range cases {
		if got := resolveRemotePath(cwd, c.arg); got != c.want {
			t.Errorf("resolveRemotePath(%q,%q)=%q want %q", cwd, c.arg, got, c.want)
		}
	}
}
