package cliclient

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// binaryDaemon is a fake daemon for the chunk-transfer tests. It
// answers the capabilities probe (advertising binary_chunks or not),
// accumulates uploaded chunks in whichever encoding arrives, and can
// script a download reply honoring the request's binary flag.
type binaryDaemon struct {
	t         *testing.T
	advertise bool
	muteCaps  bool // swallow CapabilitiesRequest (simulates unresponsive probe)

	mu        sync.Mutex
	capsReqs  int
	uploaded  []byte
	sawBinUp  bool
	sawJSONUp bool
	dlBinary  bool
}

func (d *binaryDaemon) loop(ctx context.Context, srv *wss.Conn, inner proto.Frame) {
	switch inner.Type {
	case proto.FrameTypeCapabilitiesRequest:
		d.mu.Lock()
		d.capsReqs++
		mute := d.muteCaps
		d.mu.Unlock()
		if mute {
			return
		}
		p, _ := capabilities.Capabilities{DaemonName: "fake", BinaryChunks: d.advertise}.Encode()
		sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeCapabilitiesResponse, ReqID: inner.ReqID, Payload: p})
	case proto.FrameTypeUploadRequest:
		d.mu.Lock()
		d.uploaded = nil
		d.mu.Unlock()
	case proto.FrameTypeFileChunkBin:
		c, err := fileproto.DecodeChunkBinary(inner.Payload)
		if err != nil {
			d.t.Errorf("decode binary chunk: %v", err)
			return
		}
		d.collect(srv, inner.ReqID, c, true)
	case proto.FrameTypeFileChunk:
		c, err := fileproto.DecodeFileChunk(inner.Payload)
		if err != nil {
			d.t.Errorf("decode json chunk: %v", err)
			return
		}
		d.collect(srv, inner.ReqID, c, false)
	case proto.FrameTypeStatRequest:
		// Advertise a tiny file: below parallelDownloadThreshold, so
		// the downloads under test stay on the sequential path.
		p, _ := fsproto.StatResponse{Entry: &fsproto.DirEntry{Type: "file", Size: 7}}.Encode()
		sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeStatResponse, ReqID: inner.ReqID, Payload: p})
	case proto.FrameTypeDownloadRequest:
		req, err := fileproto.DecodeDownloadRequest(inner.Payload)
		if err != nil {
			d.t.Errorf("decode download request: %v", err)
			return
		}
		d.mu.Lock()
		d.dlBinary = req.Binary
		d.mu.Unlock()
		data := []byte("NEWDATA")
		if req.Binary {
			sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeFileChunkBin, ReqID: inner.ReqID,
				Payload: fileproto.EncodeChunkBinary(fileproto.FileChunk{Data: data[:3]})})
			sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeFileChunkBin, ReqID: inner.ReqID,
				Payload: fileproto.EncodeChunkBinary(fileproto.FileChunk{Data: data[3:], Eof: true})})
		} else {
			sendChunk(d.t, srv, inner.ReqID, data[:3], false)
			sendChunk(d.t, srv, inner.ReqID, data[3:], true)
		}
		p, _ := fileproto.FileComplete{Size: int64(len(data)), Sha256: "aa"}.Encode()
		sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: inner.ReqID, Payload: p})
	}
}

func (d *binaryDaemon) collect(srv *wss.Conn, reqID string, c fileproto.FileChunk, bin bool) {
	d.mu.Lock()
	d.uploaded = append(d.uploaded, c.Data...)
	if bin {
		d.sawBinUp = true
	} else {
		d.sawJSONUp = true
	}
	size := len(d.uploaded)
	eof := c.Eof
	d.mu.Unlock()
	if eof {
		p, _ := fileproto.FileComplete{Size: int64(size), Sha256: "aa"}.Encode()
		sendRoute(d.t, srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: p})
	}
}

// chunkFixture wires a disabled-e2e Router to a binaryDaemon.
func chunkFixture(t *testing.T, advertise bool) (*Router, context.Context, context.CancelFunc, *binaryDaemon) {
	t.Helper()
	client, server := connPair(t)
	key := []byte("testkey")
	ctx, cancel := context.WithCancel(context.Background())
	r := &Router{ctx: ctx, conn: client, apiKey: key, e2eMode: "disabled", pending: make(map[string]*pendingCall)}
	go r.runReader()
	d := &binaryDaemon{t: t, advertise: advertise}
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
			d.loop(ctx, server, inner)
		}
	}()
	return r, ctx, cancel, d
}

func uploadChunks(t *testing.T, r *Router, ctx context.Context, payload string) any {
	t.Helper()
	res, err := r.UploadHandler(ctx, mustJSON(uploadArgs{
		DestPath:  "/remote/f.bin",
		Source:    "chunks",
		ChunksB64: []string{base64.StdEncoding.EncodeToString([]byte(payload))},
	}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	return res
}

// New daemons advertise binary_chunks, so the bridge must send upload
// chunks as FrameTypeFileChunkBin — and probe capabilities only once
// per connection (result cached).
func TestUploadBinaryChunksWhenAdvertised(t *testing.T) {
	r, ctx, cancel, d := chunkFixture(t, true)
	defer cancel()

	if res := uploadChunks(t, r, ctx, "AAABBB"); isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	d.mu.Lock()
	got, bin, jsn, caps := string(d.uploaded), d.sawBinUp, d.sawJSONUp, d.capsReqs
	d.mu.Unlock()
	if got != "AAABBB" {
		t.Errorf("uploaded = %q, want AAABBB", got)
	}
	if !bin || jsn {
		t.Errorf("chunk encodings: sawBin=%v sawJSON=%v, want bin only", bin, jsn)
	}
	if caps != 1 {
		t.Errorf("capabilities probes = %d, want 1 (cached)", caps)
	}

	// Second upload must reuse the cached capability answer.
	if res := uploadChunks(t, r, ctx, "CCC"); isErrorResult(res) {
		t.Fatalf("second upload errored: %v", res)
	}
	d.mu.Lock()
	got, caps = string(d.uploaded), d.capsReqs
	d.mu.Unlock()
	if got != "CCC" || caps != 1 {
		t.Errorf("second upload: content=%q capsReqs=%d, want CCC/1", got, caps)
	}
}

// Old daemons (no binary_chunks flag) must keep receiving JSON chunks —
// sending them binary frames would silently hang the transfer.
func TestUploadJsonChunksWhenNotAdvertised(t *testing.T) {
	r, ctx, cancel, d := chunkFixture(t, false)
	defer cancel()

	if res := uploadChunks(t, r, ctx, "HELLO"); isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	d.mu.Lock()
	got, bin, jsn := string(d.uploaded), d.sawBinUp, d.sawJSONUp
	d.mu.Unlock()
	if got != "HELLO" {
		t.Errorf("uploaded = %q, want HELLO", got)
	}
	if bin || !jsn {
		t.Errorf("chunk encodings: sawBin=%v sawJSON=%v, want JSON only", bin, jsn)
	}
}

// Downloads always request binary chunks; the fake honors it and the
// bridge must reassemble the binary frames. The old-JSON reply path is
// already covered by TestDownloadToDestCommitsAtomically (the fake
// ignores the binary flag and sends JSON chunks).
func TestDownloadReassemblesBinaryChunks(t *testing.T) {
	r, ctx, cancel, d := chunkFixture(t, true)
	defer cancel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/x", DestPath: dest}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if isErrorResult(res) {
		t.Fatalf("unexpected error result: %v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "NEWDATA" {
		t.Errorf("dest content = %q, %v; want NEWDATA", got, err)
	}
	d.mu.Lock()
	reqBin := d.dlBinary
	d.mu.Unlock()
	if !reqBin {
		t.Error("download request should carry binary=true")
	}
}

// An unresponsive capabilities probe must not poison the gate: the
// first call falls back to false, and a later call retries (an old
// daemon answering after a transient hiccup still gets used).
func TestBinaryProbeFailureNotCached(t *testing.T) {
	r, ctx, cancel, d := chunkFixture(t, true)
	defer cancel()

	d.mu.Lock()
	d.muteCaps = true
	d.mu.Unlock()

	shortCtx, shortCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer shortCancel()
	if r.supportsBinaryChunks(shortCtx) {
		t.Fatal("mute probe must report false")
	}

	d.mu.Lock()
	d.muteCaps = false
	d.mu.Unlock()
	if !r.supportsBinaryChunks(ctx) {
		t.Fatal("retry after failure must see advertised flag")
	}
}
