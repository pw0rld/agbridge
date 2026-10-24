package agentsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// startServer brings up Serve on a fresh socket and returns the
// socket path. The server is cancelled (and verified to exit) via
// t.Cleanup. The dir comes from os.MkdirTemp rather than t.TempDir:
// macOS sun_path caps UDS paths at ~104 bytes and TempDir names with
// long test names exceed that.
func startServer(t *testing.T, h Handler, opts ServerOptions) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ags")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, sock, h, opts) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		select {
		case err := <-errCh:
			t.Fatalf("Serve exited early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agentsock: socket never appeared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return sock
}

func call(t *testing.T, sock string, req Request) (Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	resp, handled, err := Call(ctx, conn, req)
	if !handled {
		t.Fatalf("Call reported unhandled with err %v", err)
	}
	return resp, err
}

func TestRoundTripAndPermissions(t *testing.T) {
	h := func(_ context.Context, req Request) (any, error) {
		var a struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return nil, err
		}
		return map[string]any{"double": a.N * 2}, nil
	}
	sock := startServer(t, h, ServerOptions{})

	if st, err := os.Stat(sock); err != nil {
		t.Fatalf("stat socket: %v", err)
	} else if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket perm = %o, want 600", perm)
	}
	if st, err := os.Stat(filepath.Dir(sock)); err != nil {
		t.Fatalf("stat dir: %v", err)
	} else if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir perm = %o, want 700", perm)
	}

	resp, err := call(t, sock, Request{ID: NewID(), Handler: "calc", Args: json.RawMessage(`{"n":21}`), TimeoutMs: 2000})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp not OK: %s", resp.Error)
	}
	var out struct {
		Double int `json:"double"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out.Double != 42 {
		t.Errorf("double = %d, want 42", out.Double)
	}
}

func TestHandlerErrorTravelsAsNotOK(t *testing.T) {
	sock := startServer(t, func(context.Context, Request) (any, error) {
		return nil, errors.New("boom")
	}, ServerOptions{})
	resp, err := call(t, sock, Request{ID: NewID(), Handler: "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.OK || resp.Error != "boom" {
		t.Fatalf("resp = %+v, want OK=false error=boom", resp)
	}
}

func TestPerRequestTimeout(t *testing.T) {
	sock := startServer(t, func(ctx context.Context, _ Request) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, ServerOptions{})
	resp, err := call(t, sock, Request{ID: NewID(), Handler: "slow", TimeoutMs: 50})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "deadline exceeded") {
		t.Fatalf("resp = %+v, want deadline-exceeded error", resp)
	}
}

func TestConcurrentClients(t *testing.T) {
	sock := startServer(t, func(_ context.Context, req Request) (any, error) {
		return map[string]any{"echo_id": req.ID}, nil
	}, ServerOptions{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := NewID()
			resp, err := call(t, sock, Request{ID: id, Handler: "echo"})
			if err != nil {
				t.Errorf("Call: %v", err)
				return
			}
			var out struct {
				EchoID string `json:"echo_id"`
			}
			if err := json.Unmarshal(resp.Result, &out); err != nil || out.EchoID != id {
				t.Errorf("echo mismatch: %+v err=%v (want %s)", out, err, id)
			}
		}()
	}
	wg.Wait()
}

func TestDialNoSocketFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := Dial(missing); err == nil {
		t.Fatal("Dial on missing socket should fail")
	}
}

func TestBadRequestLineAnswered(t *testing.T) {
	sock := startServer(t, func(context.Context, Request) (any, error) {
		return "unused", nil
	}, ServerOptions{})
	conn, err := Dial(sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sc := bufio.NewScanner(conn)
	if !sc.Scan() {
		t.Fatalf("no response line: %v", sc.Err())
	}
	var resp Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "bad request") {
		t.Fatalf("resp = %+v, want bad-request error", resp)
	}
}

func TestSocketRemovedOnExit(t *testing.T) {
	dir, err := os.MkdirTemp("", "ags")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, sock, func(context.Context, Request) (any, error) { return nil, nil }, ServerOptions{})
	}()
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %s should be removed after Serve returns (err=%v)", sock, err)
	}
}
