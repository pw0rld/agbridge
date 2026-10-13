package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

// matchLines returns the 1-indexed line numbers where needle starts within
// body. Used by edit_file ambiguous_match error to tell the AI exactly
// which lines clashed, so it can extend old_string with surrounding
// context to make the match unique.
func matchLines(body, needle string) []int {
	var lines []int
	if needle == "" {
		return lines
	}
	offset := 0
	for offset < len(body) {
		i := strings.Index(body[offset:], needle)
		if i < 0 {
			break
		}
		absIdx := offset + i
		line := 1 + strings.Count(body[:absIdx], "\n")
		lines = append(lines, line)
		offset = absIdx + len(needle)
	}
	return lines
}

func joinInts(xs []int, sep string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, sep)
}

// EditFile performs a single old_string → new_string replacement on the
// file at req.Path. If ReplaceAll is false, exactly one occurrence must
// match; multiple or zero matches return ambiguous_match or
// old_string_not_found respectively without touching the file. When
// ReplaceAll is true, all occurrences are replaced (zero still returns
// old_string_not_found).
//
// The write is atomic via temp + rename in the same directory.
// maxBytes caps both the read and the resulting file size; a file
// larger than maxBytes returns exceeds_max_size before any read.
//
// Returned FileComplete uses these codes:
//   not_found, exceeds_max_size, bad_payload,
//   old_string_not_found, ambiguous_match, open_failed, read_failed,
//   tmp_create_failed, write_failed, rename_failed, close_failed.
//
// Error (the Go return) is only set for ctx cancellation; everything
// else is encoded in FileComplete.Err for the bridge to surface.
func EditFile(ctx context.Context, req fileproto.FileEditRequest, maxBytes int64) (fileproto.FileComplete, error) {
	if req.OldString == "" {
		return fileproto.FileComplete{
			Err:       "bad_payload",
			ErrDetail: "old_string must not be empty (would match every position)",
		}, nil
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileproto.FileComplete{Err: "not_found"}, nil
		}
		return fileproto.FileComplete{Err: "open_failed"}, nil
	}
	if info.Size() > maxBytes {
		return fileproto.FileComplete{Size: info.Size(), Err: "exceeds_max_size"}, nil
	}

	// Read full file. EditFile is for small/medium source files (a
	// 10 MB cap fits any file an AI agent should be hand-editing).
	src, err := os.Open(req.Path)
	if err != nil {
		return fileproto.FileComplete{Err: "open_failed"}, nil
	}
	contents, err := io.ReadAll(io.LimitReader(src, maxBytes+1))
	_ = src.Close()
	if err != nil {
		return fileproto.FileComplete{Err: "read_failed"}, nil
	}
	if int64(len(contents)) > maxBytes {
		return fileproto.FileComplete{Size: int64(len(contents)), Err: "exceeds_max_size"}, nil
	}
	if ctx.Err() != nil {
		return fileproto.FileComplete{}, ctx.Err()
	}

	body := string(contents)
	count := strings.Count(body, req.OldString)
	if count == 0 {
		return fileproto.FileComplete{Err: "old_string_not_found"}, nil
	}
	if count > 1 && !req.ReplaceAll {
		lines := matchLines(body, req.OldString)
		return fileproto.FileComplete{
			Err: "ambiguous_match",
			ErrDetail: fmt.Sprintf(
				"old_string occurs %d times (at lines %s); pass replace_all=true or extend old_string with surrounding context to make the match unique",
				count, joinInts(lines, ", "),
			),
		}, nil
	}

	var next string
	if req.ReplaceAll {
		next = strings.ReplaceAll(body, req.OldString, req.NewString)
	} else {
		next = strings.Replace(body, req.OldString, req.NewString, 1)
	}

	// Backup original before atomic rewrite. AI agents misjudge old_string
	// context routinely; .bak gives operators a one-shot undo path. Filename
	// embeds a content hash so concurrent edits don't collide, and timestamp
	// so the operator can pick the right one.
	backupPath := ""
	if len(contents) > 0 {
		stamp := time.Now().UTC().Format("20060102T150405")
		h := sha256.Sum256(contents)
		backupPath = req.Path + ".bak." + stamp + "." + hex.EncodeToString(h[:4])
		if err := os.WriteFile(backupPath, contents, info.Mode().Perm()); err != nil {
			// Non-fatal: log via err return path but don't block the edit.
			// AI agents would rather have an edit succeed without backup
			// than lose all guarantees. Backup failures are rare (disk
			// full or perms).
			backupPath = ""
		}
	}

	// Atomic: write tmp in same dir, fsync, rename. Same pattern as
	// internal/tools/writefile.go to keep failure semantics consistent.
	dir := filepath.Dir(req.Path)
	tmp, err := os.CreateTemp(dir, ".agbridge-edit-*")
	if err != nil {
		return fileproto.FileComplete{Err: "tmp_create_failed"}, nil
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.WriteString(next); err != nil {
		_ = tmp.Close()
		cleanup()
		return fileproto.FileComplete{Err: "write_failed"}, nil
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fileproto.FileComplete{Err: "write_failed"}, nil
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fileproto.FileComplete{Err: "close_failed"}, nil
	}
	// Preserve original mode.
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		// non-fatal: continue with whatever mode CreateTemp used.
		_ = err
	}
	if err := os.Rename(tmpPath, req.Path); err != nil {
		cleanup()
		return fileproto.FileComplete{Err: "rename_failed"}, nil
	}

	// GC old backups so successive edits don't leave an unbounded trail
	// behind. Keep the most recent BackupRetainCount (default 5) and
	// delete the rest. Best-effort: failures here don't break the edit.
	pruneOldBackups(req.Path, BackupRetainCount)

	h := sha256.Sum256([]byte(next))
	return fileproto.FileComplete{
		Size:       int64(len(next)),
		Sha256:     hex.EncodeToString(h[:]),
		BackupPath: backupPath,
	}, nil
}

// BackupRetainCount is the per-file cap on .bak.* files; older backups
// are deleted when each successful edit creates a new one. v0.3 default
// 5 — enough for an undo trail without unbounded disk growth.
const BackupRetainCount = 5

// pruneOldBackups deletes the oldest .bak.* siblings of path, keeping
// at most keep. Determined by mtime descending so the most recent
// backup files (whichever they are) survive.
func pruneOldBackups(path string, keep int) {
	if keep <= 0 {
		return
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) <= keep {
		return
	}
	type entry struct {
		name string
		mod  int64
	}
	entries := make([]entry, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		entries = append(entries, entry{name: m, mod: info.ModTime().UnixNano()})
	}
	// Sort descending by mtime (newest first); delete everything past keep.
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })
	for i := keep; i < len(entries); i++ {
		_ = os.Remove(entries[i].name)
	}
}
