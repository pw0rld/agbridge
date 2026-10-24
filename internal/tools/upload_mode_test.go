package tools

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestUploadTempMasksSpecialModeBits(t *testing.T) {
	dir := t.TempDir()
	_, tmpPath, err := openUploadTemp(filepath.Join(dir, "sub", "f.bin"), 0o4755)
	if err != nil {
		t.Fatalf("openUploadTemp: %v", err)
	}
	defer os.Remove(tmpPath)
	info, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("perm: %v, want 755", info.Mode().Perm())
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("special bits survived: %v", info.Mode())
	}
}

func TestUploadFinalizeMasksSpecialModeBits(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "final.bin")
	tmp, tmpPath, err := openUploadTemp(dest, 0)
	if err != nil {
		t.Fatalf("openUploadTemp: %v", err)
	}
	body := []byte("data")
	if _, err := tmp.Write(body); err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	hasher.Write(body)
	complete, err := finalizeUpload(tmp, tmpPath, dest, 0o7777, int64(len(body)), hasher, 0, "")
	if err != nil {
		t.Fatalf("finalizeUpload: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("complete.Err: %q / %q", complete.Err, complete.ErrDetail)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Errorf("perm: %v, want 777", info.Mode().Perm())
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("special bits survived: %v", info.Mode())
	}
}
