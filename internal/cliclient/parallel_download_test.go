package cliclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// segDaemon emulates the real daemon for segmented-download tests: it
// serves a fixed blob over concurrent DownloadRequests honoring
// Offset/Limit (one goroutine per request, matching the daemon's
// dispatch), records each requested window, and can fail selected
// windows to exercise the fallback path.
type segDaemon struct {
	t    *testing.T
	blob []byte

	mu     sync.Mutex
	wins   [][2]int64 // {Offset, Limit} per DownloadRequest
	statRe int
	dlReq  int
	fail   func(off, ln int64) bool

	sendMu sync.Mutex // one writer per wss conn, like the real sendMu
}

func (d *segDaemon) send(srv *wss.Conn, f proto.Frame) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	sendRoute(d.t, srv, f)
}

func (d *segDaemon) loop(srv *wss.Conn, inner proto.Frame) {
	switch inner.Type {
	case proto.FrameTypeStatRequest:
		d.mu.Lock()
		d.statRe++
		n := int64(len(d.blob))
		d.mu.Unlock()
		p, _ := fsproto.StatResponse{Entry: &fsproto.DirEntry{Type: "file", Size: n}}.Encode()
		d.send(srv, proto.Frame{Type: proto.FrameTypeStatResponse, ReqID: inner.ReqID, Payload: p})
	case proto.FrameTypeDownloadRequest:
		req, err := fileproto.DecodeDownloadRequest(inner.Payload)
		if err != nil {
			d.t.Errorf("decode download request: %v", err)
			return
		}
		d.mu.Lock()
		d.dlReq++
		d.wins = append(d.wins, [2]int64{req.Offset, req.Limit})
		failed := d.fail != nil && d.fail(req.Offset, req.Limit)
		d.mu.Unlock()
		go d.serve(srv, inner.ReqID, req, failed)
	}
}

func (d *segDaemon) serve(srv *wss.Conn, reqID string, req fileproto.DownloadRequest, failed bool) {
	if failed {
		p, _ := fileproto.FileComplete{Err: "read_failed", ErrDetail: "simulated"}.Encode()
		d.send(srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: p})
		return
	}
	lo := req.Offset
	if lo > int64(len(d.blob)) {
		lo = int64(len(d.blob))
	}
	hi := int64(len(d.blob))
	if req.Limit > 0 && lo+req.Limit < hi {
		hi = lo + req.Limit
	}
	data := d.blob[lo:hi]
	const chunkSize = 48 << 10 // small enough for several chunks per segment
	for off := 0; off < len(data); off += chunkSize {
		end := off + chunkSize
		if end > len(data) {
			end = len(data)
		}
		d.send(srv, proto.Frame{Type: proto.FrameTypeFileChunkBin, ReqID: reqID,
			Payload: fileproto.EncodeChunkBinary(fileproto.FileChunk{Data: data[off:end]})})
	}
	d.send(srv, proto.Frame{Type: proto.FrameTypeFileChunkBin, ReqID: reqID,
		Payload: fileproto.EncodeChunkBinary(fileproto.FileChunk{Eof: true})})
	sum := sha256.Sum256(data)
	p, _ := fileproto.FileComplete{Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}.Encode()
	d.send(srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: p})
}

// segFixture wires a disabled-e2e Router to a segDaemon.
func segFixture(t *testing.T, blob []byte) (*Router, context.Context, context.CancelFunc, *segDaemon) {
	t.Helper()
	client, server := connPair(t)
	key := []byte("testkey")
	ctx, cancel := context.WithCancel(context.Background())
	r := &Router{ctx: ctx, conn: client, apiKey: key, e2eMode: "disabled", pending: make(map[string]chan proto.Frame)}
	go r.runReader()
	d := &segDaemon{t: t, blob: blob}
	go func() {
		for {
			f, err := server.Recv(ctx)
			if err != nil {
				return
			}
			plain, err := auth.VerifyFrame(key, f.Payload)
			if err != nil {
				return
			}
			inner, err := proto.Decode(plain)
			if err != nil {
				return
			}
			d.loop(server, inner)
		}
	}()
	return r, ctx, cancel, d
}

// makeBlob builds a position-dependent pattern so any mis-assembly of
// segments (wrong offset, swapped ranges) is detected.
func makeBlob(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i/1000)
	}
	return b
}

// lowerThreshold points parallelDownloadThreshold at a test-sized file
// and restores it afterwards.
func lowerThreshold(t *testing.T, v int64) {
	t.Helper()
	old := parallelDownloadThreshold
	parallelDownloadThreshold = v
	t.Cleanup(func() { parallelDownloadThreshold = old })
}

// assertTiled checks the recorded {offset, limit} windows tile [0,size)
// exactly once each: no gaps, no overlaps, no positive-limit segments.
func assertTiled(t *testing.T, wins [][2]int64, size int64) {
	t.Helper()
	sort.Slice(wins, func(i, j int) bool { return wins[i][0] < wins[j][0] })
	var pos int64
	for _, w := range wins {
		if w[0] != pos {
			t.Errorf("window %+v starts at %d, want %d (gap/overlap)", w, w[0], pos)
		}
		if w[1] <= 0 {
			t.Errorf("segment window %+v has non-positive limit", w)
		}
		pos += w[1]
	}
	if pos != size {
		t.Errorf("segments cover %d bytes, want %d", pos, size)
	}
}

// A whole-file download of a "large" file must be fetched as concurrent
// segments that tile the file exactly, land byte-identical at dest, and
// report the segment count in metadata.
func TestParallelDownloadSegments(t *testing.T) {
	lowerThreshold(t, 1<<20)
	blob := makeBlob(3 << 20) // 3 MiB / 8 MiB target → clamped to segmentsMin
	r, ctx, cancel, d := segFixture(t, blob)
	defer cancel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/big.bin", DestPath: dest}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("assembled content mismatch: got %d bytes, want %d", len(got), len(blob))
	}

	d.mu.Lock()
	wins := append([][2]int64(nil), d.wins...)
	statRe := d.statRe
	d.mu.Unlock()

	if len(wins) != parallelSegmentsMin {
		t.Fatalf("download requests = %d, want %d segments: %v", len(wins), parallelSegmentsMin, wins)
	}
	assertTiled(t, wins, int64(len(blob)))
	if statRe != 1 {
		t.Errorf("stat probes = %d, want 1", statRe)
	}
	meta := res.(map[string]any)["_meta"].(map[string]any)
	if meta["segments"] != parallelSegmentsMin {
		t.Errorf("_meta.segments = %v, want %d", meta["segments"], parallelSegmentsMin)
	}
	if meta["sha256"] != func() string { s := sha256.Sum256(blob); return hex.EncodeToString(s[:]) }() {
		t.Errorf("_meta.sha256 = %v, want whole-file digest", meta["sha256"])
	}
}

// A failing segment must fall back to the sequential whole-file path and
// still deliver correct content.
func TestParallelDownloadFallbackRecovers(t *testing.T) {
	lowerThreshold(t, 1<<20)
	blob := makeBlob(3 << 20)
	r, ctx, cancel, d := segFixture(t, blob)
	defer cancel()

	d.mu.Lock()
	// Segment requests always carry a positive Limit; the sequential
	// fallback requests the whole file with Limit == 0.
	d.fail = func(_, ln int64) bool { return ln > 0 }
	d.mu.Unlock()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/big.bin", DestPath: dest}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, blob) {
		t.Fatalf("dest after fallback: len=%d err=%v", len(got), err)
	}

	d.mu.Lock()
	wins := append([][2]int64(nil), d.wins...)
	d.mu.Unlock()
	if len(wins) != parallelSegmentsMin+1 {
		t.Fatalf("requests = %d, want %d segments + 1 sequential: %v", len(wins), parallelSegmentsMin, wins)
	}
	last := wins[len(wins)-1]
	if last != [2]int64{0, 0} {
		t.Errorf("final request = %v, want whole-file {0,0}", last)
	}
}

// Every request failing: parallel attempt cleans its temp, the
// sequential fallback fails too, and the pre-existing dest plus the
// directory are left untouched.
func TestParallelDownloadFallbackFailsCleanly(t *testing.T) {
	lowerThreshold(t, 1<<20)
	blob := makeBlob(3 << 20)
	r, ctx, cancel, d := segFixture(t, blob)
	defer cancel()

	d.mu.Lock()
	d.fail = func(int64, int64) bool { return true }
	d.mu.Unlock()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dest, []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/big.bin", DestPath: dest}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !isErrorResult(res) {
		t.Fatalf("expected error result, got %v", res)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "OLD" {
		t.Errorf("dest was clobbered: %q", got)
	}
	if names := dirEntries(t, dir); len(names) != 1 || names[0] != "out.bin" {
		t.Errorf("temp leftovers after failure: %v", names)
	}
}

// Offset/Limit requests bypass the stat probe and the parallel path
// entirely (they already describe a partial fetch).
func TestDownloadOffsetLimitSkipsStat(t *testing.T) {
	lowerThreshold(t, 1<<20) // would parallelize if the gate were wrong
	blob := makeBlob(3 << 20)
	r, ctx, cancel, d := segFixture(t, blob)
	defer cancel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/big.bin", DestPath: dest, Offset: 100, Limit: 5000}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, blob[100:5100]) {
		t.Fatalf("range content mismatch: len=%d err=%v", len(got), err)
	}

	d.mu.Lock()
	statRe, wins := d.statRe, append([][2]int64(nil), d.wins...)
	d.mu.Unlock()
	if statRe != 0 {
		t.Errorf("stat probes = %d, want 0 for ranged download", statRe)
	}
	if len(wins) != 1 || wins[0] != [2]int64{100, 5000} {
		t.Errorf("requests = %v, want single {100,5000}", wins)
	}
}
