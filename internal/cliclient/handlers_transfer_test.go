package cliclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// connPair wires two *wss.Conn over an in-process TLS websocket,
// keepalive disabled (these tests are short and deterministic).
func connPair(t *testing.T) (client, server *wss.Conn) {
	t.Helper()
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	upgrader := websocket.Upgrader{}
	got := make(chan *websocket.Conn, 1)
	hs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		got <- ws
		<-r.Context().Done()
	}))
	hs.TLS = srvCfg
	hs.StartTLS()
	t.Cleanup(hs.Close)

	d := websocket.Dialer{TLSClientConfig: cliCfg}
	u, _ := url.Parse(hs.URL)
	u.Scheme = "wss"
	cws, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sws := <-got
	dis := wss.KeepaliveOpts{Disable: true}
	client = wss.NewConnWithKeepalive(cws, transport.Identity{}, dis)
	server = wss.NewConnWithKeepalive(sws, transport.Identity{}, dis)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return
}

// downloadFixture builds a disabled-e2e Router on a fresh conn pair plus
// a fake daemon that answers size probes with statSize (below
// parallelDownloadThreshold keeps tests on the sequential path) and runs
// script(reqID) for every DownloadRequest that arrives.
func downloadFixture(t *testing.T, statSize int64, script func(ctx context.Context, srv *wss.Conn, reqID string)) (*Router, context.Context, context.CancelFunc) {
	t.Helper()
	client, server := connPair(t)
	key := []byte("testkey")
	ctx, cancel := context.WithCancel(context.Background())
	r := &Router{ctx: ctx, conn: client, apiKey: key, e2eMode: "disabled", pending: make(map[string]*pendingCall)}
	go r.runReader()

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
			switch inner.Type {
			case proto.FrameTypeStatRequest:
				p, _ := fsproto.StatResponse{Entry: &fsproto.DirEntry{Type: "file", Size: statSize}}.Encode()
				sendRoute(t, server, proto.Frame{Type: proto.FrameTypeStatResponse, ReqID: inner.ReqID, Payload: p})
			case proto.FrameTypeDownloadRequest:
				script(ctx, server, inner.ReqID)
			}
		}
	}()
	return r, ctx, cancel
}

func sendRoute(t *testing.T, c *wss.Conn, inner proto.Frame) {
	t.Helper()
	b, err := inner.Encode()
	if err != nil {
		t.Errorf("encode inner: %v", err)
		return
	}
	if err := c.Send(context.Background(), proto.Frame{Type: proto.FrameTypeRoute, Payload: b}); err != nil {
		t.Errorf("send route: %v", err)
	}
}

func sendChunk(t *testing.T, srv *wss.Conn, reqID string, data []byte, eof bool) {
	p, _ := fileproto.FileChunk{Data: data, Eof: eof}.Encode()
	sendRoute(t, srv, proto.Frame{Type: proto.FrameTypeFileChunk, ReqID: reqID, Payload: p})
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func isErrorResult(res any) bool {
	m, ok := res.(map[string]any)
	if !ok {
		return false
	}
	is, _ := m["isError"].(bool)
	return is
}

// Success: content lands at dest atomically; an existing file keeps its
// permissions; no temp file remnants.
func TestDownloadToDestCommitsAtomically(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dest, []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}

	r, ctx, cancel := downloadFixture(t, 7, func(ctx context.Context, srv *wss.Conn, reqID string) {
		sendChunk(t, srv, reqID, []byte("NEW"), false)
		sendChunk(t, srv, reqID, []byte("DATA"), true)
		p, _ := fileproto.FileComplete{Size: 7, Sha256: "aa"}.Encode()
		sendRoute(t, srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: p})
	})
	defer cancel()

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
	fi, err := os.Stat(dest)
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("dest mode = %v, %v; want preserved 0640", fi.Mode(), err)
	}
	if names := dirEntries(t, dir); len(names) != 1 || names[0] != "out.bin" {
		t.Errorf("dir contains leftovers: %v", names)
	}
}

// Daemon-reported failure mid-download: old file untouched, temp removed.
func TestDownloadErrorKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dest, []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}

	r, ctx, cancel := downloadFixture(t, 7, func(ctx context.Context, srv *wss.Conn, reqID string) {
		sendChunk(t, srv, reqID, []byte("PARTIAL"), false)
		p, _ := fileproto.FileComplete{Err: "read_failed", ErrDetail: "disk"}.Encode()
		sendRoute(t, srv, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: p})
	})
	defer cancel()

	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/x", DestPath: dest}))
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
		t.Errorf("temp not cleaned up: %v", names)
	}
}

// Cancellation mid-download: same guarantees as the error path.
func TestDownloadCancelKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dest, []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}

	r, ctx, cancel := downloadFixture(t, 7, func(ctx context.Context, srv *wss.Conn, reqID string) {
		sendChunk(t, srv, reqID, []byte("PART"), false)
		// then go silent forever — the test cancels ctx
		<-ctx.Done()
	})
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	res, err := r.DownloadHandler(ctx, mustJSON(downloadArgs{SrcPath: "/x", DestPath: dest}))
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
		t.Errorf("temp not cleaned up: %v", names)
	}
}
