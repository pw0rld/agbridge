package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/state"
)

// A protocol peer, not a local daemon: this fixture also runs on Windows.
// Exercise the real CLI, TLS, enrollment, Noise IK and local file IO while
// asserting that the remote paths retain their POSIX semantics.
func TestCLIPlatformRoundTrip(t *testing.T) {
	key, err := e2e.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var uploaded []byte
	var bridgePub string
	var gatewayURL, pin string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req gateway.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
			http.Error(w, "token required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bridgePub = req.DevicePubkeyB64
		mu.Unlock()
		target := "lab"
		if req.Token == "daemon-token" {
			target = ""
		}
		_ = json.NewEncoder(w).Encode(gateway.EnrollResponse{
			DeviceID: "bridge-test", Name: "windows-client", APIKey: "test-key",
			CertPin: pin, TargetDaemon: target, DaemonPubkey: key.PubBase64(), E2EMode: "required",
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()
		_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		receive := func() (proto.Frame, error) {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return proto.Frame{}, err
			}
			return proto.Decode(data)
		}
		send := func(f proto.Frame) {
			data, err := f.Encode()
			if err == nil {
				err = ws.WriteMessage(websocket.BinaryMessage, data)
			}
			if err != nil {
				t.Error(err)
			}
		}
		f, err := receive()
		if err != nil {
			t.Error(err)
			return
		}
		h, err := handshake.DecodeHello(f.Payload)
		if err != nil || f.Type != proto.FrameTypeHello || h.Role != "bridge" || h.Secret != "test-key" || h.TargetDaemon != "lab" {
			t.Errorf("unexpected hello: %+v, %v", h, err)
			return
		}
		send(proto.Frame{Type: proto.FrameTypeHelloAck})
		f, err = receive()
		if err != nil || f.Type != proto.FrameTypeNoiseInit {
			t.Errorf("NoiseInit: %v, %v", f.Type, err)
			return
		}
		sid, err := hex.DecodeString(f.ReqID)
		if err != nil || len(sid) != 8 {
			t.Errorf("bad session ID: %q", f.ReqID)
			return
		}
		responder, err := e2e.NewResponder(key, []byte("agbridge/v1|"+gatewayURL+"|lab"))
		if err != nil {
			t.Error(err)
			return
		}
		peer, err := responder.ReadMessage1(f.Payload)
		mu.Lock()
		wantPeer, decodeErr := e2e.PubFromBase64(bridgePub)
		mu.Unlock()
		if err != nil || decodeErr != nil || !bytes.Equal(peer, wantPeer) {
			t.Errorf("Noise peer doesn't match enrolled key: %v, %v", err, decodeErr)
			return
		}
		msg, session, err := responder.WriteMessage2()
		if err != nil {
			t.Error(err)
			return
		}
		send(proto.Frame{Type: proto.FrameTypeNoiseResp, ReqID: f.ReqID, Payload: msg})
		reply := func(reqID string, typ proto.FrameType, body any) {
			payload, _ := json.Marshal(body)
			inner, _ := (proto.Frame{Type: typ, ReqID: reqID, Payload: payload}).Encode()
			ct, err := session.Encrypt(sid, inner)
			if err != nil {
				t.Error(err)
				return
			}
			send(proto.Frame{Type: proto.FrameTypeRoute, Payload: append(append([]byte{}, sid...), ct...)})
		}
		for {
			f, err = receive()
			if err != nil {
				return // One-shot CLI closes after each command, including status.
			}
			f.Payload, err = auth.VerifyFrame([]byte("test-key"), f.Payload)
			if err != nil {
				t.Error(err)
				return
			}
			if f.Type != proto.FrameTypeRoute || len(f.Payload) < 8 || !bytes.Equal(f.Payload[:8], sid) {
				t.Errorf("unexpected route envelope: %v", f.Type)
				return
			}
			plain, err := session.Decrypt(sid, f.Payload[8:])
			if err != nil {
				t.Error(err)
				return
			}
			inner, err := proto.Decode(plain)
			if err != nil {
				t.Error(err)
				return
			}
			switch inner.Type {
			case proto.FrameTypeCapabilitiesRequest:
				reply(inner.ReqID, proto.FrameTypeCapabilitiesResponse, capabilities.Capabilities{DefaultWorkingDir: "/srv/project", ProtocolVersion: 2})
			case proto.FrameTypeExecRequest:
				req, err := execproto.DecodeExecRequest(inner.Payload)
				if err != nil || req.Cwd != "/srv/project/src" || req.Cmd != "/bin/echo" || len(req.Args) != 1 || req.Args[0] != "hello world" {
					t.Errorf("remote exec arguments: %+v, %v", req, err)
				}
				reply(inner.ReqID, proto.FrameTypeExecChunk, execproto.ExecChunk{Stream: "stdout", Data: []byte("hello world\n")})
				reply(inner.ReqID, proto.FrameTypeExecComplete, execproto.ExecComplete{})
			case proto.FrameTypeUploadRequest:
				req, err := fileproto.DecodeUploadRequest(inner.Payload)
				if err != nil || req.DestPath != "/srv/project/data.bin" || req.Source != "chunks" {
					t.Errorf("remote upload path: %+v, %v", req, err)
				}
				mu.Lock()
				uploaded = nil
				mu.Unlock()
			case proto.FrameTypeFileChunk:
				chunk, err := fileproto.DecodeFileChunk(inner.Payload)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				uploaded = append(uploaded, chunk.Data...)
				size, sum := len(uploaded), sha256.Sum256(uploaded)
				mu.Unlock()
				if chunk.Eof {
					reply(inner.ReqID, proto.FrameTypeFileComplete, fileproto.FileComplete{Size: int64(size), Sha256: fmt.Sprintf("%x", sum)})
				}
			case proto.FrameTypeDownloadRequest:
				req, err := fileproto.DecodeDownloadRequest(inner.Payload)
				if err != nil || req.SrcPath != "/srv/project/data.bin" {
					t.Errorf("remote download path: %+v, %v", req, err)
				}
				mu.Lock()
				data := append([]byte{}, uploaded...)
				mu.Unlock()
				reply(inner.ReqID, proto.FrameTypeFileChunk, fileproto.FileChunk{Data: data, Eof: true})
				reply(inner.ReqID, proto.FrameTypeFileComplete, fileproto.FileComplete{Size: int64(len(data)), Sha256: fmt.Sprintf("%x", sha256.Sum256(data))})
			default:
				t.Errorf("unexpected inner frame: %v", inner.Type)
				return
			}
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	gatewayURL = "wss" + strings.TrimPrefix(srv.URL, "https") + "/"
	pin = fmt.Sprintf("sha256:%x", sha256.Sum256(srv.Certificate().Raw))
	dir := filepath.Join(t.TempDir(), "client state \u6d4b\u8bd5")
	run := func(args ...string) string {
		t.Helper()
		cmd := newRootCmd()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs(append([]string{"--state-dir", dir, "--timeout", "10s", "--json"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, out.String(), stderr.String())
		}
		return out.String()
	}
	run("enroll", "--gateway", gatewayURL, "--token", "bridge-token")
	st, err := state.LoadBridge(filepath.Join(dir, "state.json"))
	if err != nil || st.DefaultTarget != "lab" || !filepath.IsAbs(st.BridgeStaticKeyPath) {
		t.Fatalf("enrolled state: %+v, %v", st, err)
	}
	if out := run("status"); !strings.Contains(out, `"connection":"ok"`) {
		t.Fatalf("status: %s", out)
	}
	// First relative-path command bootstraps cwd and overwrites state.json.
	if out := run("exec", "--cwd", "src", "--", "/bin/echo", "hello world"); !strings.Contains(out, "hello world") {
		t.Fatalf("exec output: %s", out)
	}
	data := bytes.Repeat([]byte{0, 0xff, '\r', '\n'}, 300000)
	source := filepath.Join(dir, "upload \u6d4b\u8bd5.bin")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatal(err)
	}
	run("put", source, "data.bin")
	mu.Lock()
	equal := bytes.Equal(uploaded, data)
	mu.Unlock()
	if !equal {
		t.Fatal("upload bytes changed")
	}
	dest := filepath.Join(dir, "download \u6d4b\u8bd5.bin")
	if err := os.WriteFile(dest, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("get", "data.bin", dest)
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("download bytes changed: %v", err)
	}

	if runtime.GOOS == "windows" {
		rejectedDir := filepath.Join(t.TempDir(), "rejected-daemon")
		cmd := newRootCmd()
		cmd.SetArgs([]string{"enroll", "--gateway", gatewayURL, "--token", "daemon-token", "--state-dir", rejectedDir})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "CLI client only") {
			t.Fatalf("daemon enrollment should be rejected: %v", err)
		}
		if _, err := os.Stat(rejectedDir); !os.IsNotExist(err) {
			t.Fatalf("daemon enrollment wrote state: %v", err)
		}
	}
}
