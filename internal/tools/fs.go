package tools

import (
	"errors"
	"io/fs"
	"os"

	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

// ListDirectory returns the immediate children of req.Path (non-recursive).
// Honors allowed_read_paths since directory contents are metadata leak.
//
// Returns ListDirResponse.Err set for path_forbidden, not_found,
// not_a_directory, or read_failed. The Err field is also set when the
// underlying readdir returns a partial error.
func ListDirectory(req fsproto.ListDirRequest, allowedPaths []string) fsproto.ListDirResponse {
	if !sandbox.PathAllowed(req.Path, allowedPaths) {
		return fsproto.ListDirResponse{
			Err:       "path_forbidden",
			ErrDetail: "path not in allowed_read_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}
	}
	info, err := os.Stat(req.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fsproto.ListDirResponse{Err: "not_found"}
		}
		return fsproto.ListDirResponse{Err: "read_failed"}
	}
	if !info.IsDir() {
		return fsproto.ListDirResponse{
			Err:       "not_a_directory",
			ErrDetail: "path is " + entryType(info) + ", expected directory",
		}
	}
	dirents, err := os.ReadDir(req.Path)
	if err != nil {
		return fsproto.ListDirResponse{Err: "read_failed"}
	}
	entries := make([]fsproto.DirEntry, 0, len(dirents))
	for _, d := range dirents {
		fi, err := d.Info()
		if err != nil {
			// Skip entries that disappear mid-listdir rather than failing
			// the whole call. AI agents prefer partial results over total
			// failure here.
			continue
		}
		entries = append(entries, fileInfoToEntry(d.Name(), fi))
	}
	return fsproto.ListDirResponse{Entries: entries}
}

// Stat returns metadata for a single path. Honors allowed_read_paths.
func Stat(req fsproto.StatRequest, allowedPaths []string) fsproto.StatResponse {
	if !sandbox.PathAllowed(req.Path, allowedPaths) {
		return fsproto.StatResponse{
			Err:       "path_forbidden",
			ErrDetail: "path not in allowed_read_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}
	}
	info, err := os.Lstat(req.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fsproto.StatResponse{Err: "not_found"}
		}
		return fsproto.StatResponse{Err: "read_failed"}
	}
	entry := fileInfoToEntry(info.Name(), info)
	return fsproto.StatResponse{Entry: &entry}
}

func fileInfoToEntry(name string, fi os.FileInfo) fsproto.DirEntry {
	e := fsproto.DirEntry{
		Name:    name,
		Type:    entryType(fi),
		Mode:    uint32(fi.Mode().Perm()),
		ModTime: fi.ModTime().Unix(),
	}
	if fi.Mode().IsRegular() {
		e.Size = fi.Size()
	}
	return e
}

func entryType(fi os.FileInfo) string {
	switch {
	case fi.Mode().IsRegular():
		return "file"
	case fi.Mode().IsDir():
		return "dir"
	case fi.Mode()&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "other"
	}
}
