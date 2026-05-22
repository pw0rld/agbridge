package tools

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pw0rld/agbridge/internal/bgproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

// BgBufferCap is the per-stream ring buffer size. Older bytes are dropped
// silently when capacity is exceeded; logs callers see a "truncated" flag.
const BgBufferCap = 1 << 20 // 1 MiB per stream

// BgIdleGC is how long a finished process stays in the registry before
// being garbage-collected. Tunable per-deployment if needed.
const BgIdleGC = 1 * time.Hour

// bgBuffer is a fixed-cap append-with-drop buffer. Tracks total bytes
// ever written so callers can request "give me everything since byte
// offset X" — bytes too old to still be retained surface as truncated.
type bgBuffer struct {
	mu      sync.Mutex
	data    []byte
	cap     int
	written int64
}

func newBgBuffer(cap int) *bgBuffer { return &bgBuffer{cap: cap} }

func (b *bgBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.written += int64(len(p))
	b.data = append(b.data, p...)
	if len(b.data) > b.cap {
		b.data = b.data[len(b.data)-b.cap:]
	}
	return len(p), nil
}

// SinceOffset returns retained bytes whose effective offset is >= offset,
// the current total-written counter (so the caller can pass it back next
// poll), and whether any older data was dropped (i.e. offset preceded
// the retained window).
func (b *bgBuffer) SinceOffset(offset int64) ([]byte, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if offset >= b.written {
		return nil, b.written, false
	}
	bufStart := b.written - int64(len(b.data))
	truncated := offset < bufStart
	sliceStart := int64(0)
	if !truncated {
		sliceStart = offset - bufStart
	}
	out := make([]byte, int64(len(b.data))-sliceStart)
	copy(out, b.data[sliceStart:])
	return out, b.written, truncated
}

// Tail returns the last N lines (newline-terminated). If the buffer
// contains fewer than N lines, returns whatever it has.
func (b *bgBuffer) Tail(n int) ([]byte, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.data) == 0 {
		return nil, b.written, false
	}
	// scan backward counting newlines
	cnt := 0
	for i := len(b.data) - 1; i >= 0; i-- {
		if b.data[i] == '\n' {
			cnt++
			if cnt > n {
				out := make([]byte, len(b.data)-(i+1))
				copy(out, b.data[i+1:])
				return out, b.written, b.written > int64(len(b.data))
			}
		}
	}
	out := append([]byte(nil), b.data...)
	return out, b.written, b.written > int64(len(b.data))
}

// BgProcess is one tracked background process. Public fields are read
// under p.mu; clients should use BgRegistry methods rather than touching
// directly.
type BgProcess struct {
	ID        string
	Label     string
	Cmd       string
	Args      []string
	Cwd       string
	Process   *os.Process
	Stdout    *bgBuffer
	Stderr    *bgBuffer
	StartedAt time.Time

	mu           sync.Mutex
	state        string // "running" | "exited" | "killed" | "failed"
	exitcode     int
	endedAt      time.Time
	lastOutputAt time.Time
	done         chan struct{}
}

// BgRegistry holds in-flight + recently-finished processes. Concurrency-
// safe.
type BgRegistry struct {
	mu        sync.Mutex
	processes map[string]*BgProcess
	idleGC    time.Duration
}

// NewBgRegistry creates an empty registry. Caller can optionally start
// a GC goroutine via go reg.GCLoop(ctx).
func NewBgRegistry() *BgRegistry {
	return &BgRegistry{
		processes: make(map[string]*BgProcess),
		idleGC:    BgIdleGC,
	}
}

// genProcessID returns a fresh "p_<hex8>" identifier.
func genProcessID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "p_" + hex.EncodeToString(b[:])
}

// StartBg spawns a new background process and registers it. Returns the
// newly-assigned process_id. Cwd is checked against allowedCwds before
// spawn.
func (r *BgRegistry) StartBg(req bgproto.BgExecRequest, allowedCwds, envAllowlist []string) bgproto.BgExecResponse {
	if req.Cmd == "" {
		return bgproto.BgExecResponse{Action: "start", Err: "bad_payload", ErrDetail: "cmd required"}
	}
	if req.Cwd == "" {
		return bgproto.BgExecResponse{Action: "start", Err: "bad_payload", ErrDetail: "cwd required"}
	}
	if _, ok := sandbox.PathAllowedResolved(req.Cwd, allowedCwds); !ok {
		return bgproto.BgExecResponse{
			Action:    "start",
			Err:       "path_forbidden",
			ErrDetail: "cwd not in allowed_exec_cwds; allowed: " + sandbox.FormatAllowlist(allowedCwds),
		}
	}

	cmd := exec.Command(req.Cmd, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = buildEnv(envAllowlist, req.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return bgproto.BgExecResponse{Action: "start", Err: "spawn_failed", ErrDetail: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return bgproto.BgExecResponse{Action: "start", Err: "spawn_failed", ErrDetail: err.Error()}
	}

	if err := cmd.Start(); err != nil {
		return bgproto.BgExecResponse{Action: "start", Err: "spawn_failed", ErrDetail: err.Error()}
	}

	proc := &BgProcess{
		ID:        genProcessID(),
		Label:     req.Label,
		Cmd:       req.Cmd,
		Args:      append([]string(nil), req.Args...),
		Cwd:       req.Cwd,
		Process:   cmd.Process,
		Stdout:    newBgBuffer(BgBufferCap),
		Stderr:    newBgBuffer(BgBufferCap),
		StartedAt: time.Now(),
		state:     "running",
		done:      make(chan struct{}),
	}

	r.mu.Lock()
	r.processes[proc.ID] = proc
	r.mu.Unlock()

	// pump stdout/stderr -> ring buffers (also stamp lastOutputAt)
	go pump(stdout, proc.Stdout, proc)
	go pump(stderr, proc.Stderr, proc)

	// wait for exit, mark state
	go func() {
		err := cmd.Wait()
		proc.mu.Lock()
		proc.endedAt = time.Now()
		if proc.state == "killed" {
			// keep
		} else if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				proc.state = "exited"
				proc.exitcode = ee.ExitCode()
			} else {
				proc.state = "failed"
				proc.exitcode = -1
			}
		} else {
			proc.state = "exited"
			proc.exitcode = 0
		}
		close(proc.done)
		proc.mu.Unlock()
	}()

	return bgproto.BgExecResponse{
		Action:    "start",
		ProcessID: proc.ID,
		Label:     proc.Label,
		State:     "running",
		StartedAt: proc.StartedAt.Unix(),
	}
}

func pump(src io.ReadCloser, dst *bgBuffer, proc *BgProcess) {
	defer src.Close()
	rd := bufio.NewReader(src)
	buf := make([]byte, 4096)
	for {
		n, err := rd.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
			proc.mu.Lock()
			proc.lastOutputAt = time.Now()
			proc.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Status returns a snapshot of the named process.
func (r *BgRegistry) Status(req bgproto.BgExecRequest) bgproto.BgExecResponse {
	proc, ok := r.lookup(req.ProcessID)
	if !ok {
		return bgproto.BgExecResponse{Action: "status", Err: "not_found",
			ErrDetail: "no process named " + req.ProcessID}
	}
	return statusFrom(proc, "status")
}

func statusFrom(p *BgProcess, action string) bgproto.BgExecResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	resp := bgproto.BgExecResponse{
		Action:    action,
		ProcessID: p.ID,
		Label:     p.Label,
		State:     p.state,
		Exitcode:  p.exitcode,
		StartedAt: p.StartedAt.Unix(),
	}
	if !p.endedAt.IsZero() {
		resp.EndedAt = p.endedAt.Unix()
		resp.DurationMs = p.endedAt.Sub(p.StartedAt).Milliseconds()
	} else {
		resp.DurationMs = time.Since(p.StartedAt).Milliseconds()
	}
	if !p.lastOutputAt.IsZero() {
		resp.LastOutputAt = p.lastOutputAt.Unix()
	}
	return resp
}

// Logs returns retained stdout/stderr per the request's TailLines or
// SinceOffset. When both are zero, returns everything currently buffered.
func (r *BgRegistry) Logs(req bgproto.BgExecRequest) bgproto.BgExecResponse {
	proc, ok := r.lookup(req.ProcessID)
	if !ok {
		return bgproto.BgExecResponse{Action: "logs", Err: "not_found",
			ErrDetail: "no process named " + req.ProcessID}
	}
	var (
		outBytes, errBytes []byte
		outOff, errOff     int64
		outTrunc, errTrunc bool
	)
	if req.TailLines > 0 {
		outBytes, outOff, outTrunc = proc.Stdout.Tail(req.TailLines)
		errBytes, errOff, errTrunc = proc.Stderr.Tail(req.TailLines)
	} else {
		outBytes, outOff, outTrunc = proc.Stdout.SinceOffset(req.SinceOffset)
		errBytes, errOff, errTrunc = proc.Stderr.SinceOffset(req.SinceOffset)
	}
	// NextOffset is max of out/err totals so a single counter advances both.
	next := outOff
	if errOff > next {
		next = errOff
	}
	return bgproto.BgExecResponse{
		Action:      "logs",
		ProcessID:   proc.ID,
		Stdout:      string(outBytes),
		Stderr:      string(errBytes),
		StdoutTrunc: outTrunc,
		StderrTrunc: errTrunc,
		NextOffset:  next,
	}
}

// Kill sends a signal to the process group. Default SIGTERM; "KILL" or
// "INT" also supported.
func (r *BgRegistry) Kill(req bgproto.BgExecRequest) bgproto.BgExecResponse {
	proc, ok := r.lookup(req.ProcessID)
	if !ok {
		return bgproto.BgExecResponse{Action: "kill", Err: "not_found",
			ErrDetail: "no process named " + req.ProcessID}
	}
	sig := syscall.SIGTERM
	switch strings.ToUpper(req.Signal) {
	case "KILL":
		sig = syscall.SIGKILL
	case "INT":
		sig = syscall.SIGINT
	case "", "TERM":
		sig = syscall.SIGTERM
	default:
		return bgproto.BgExecResponse{Action: "kill", Err: "bad_payload",
			ErrDetail: "signal must be TERM, KILL, or INT"}
	}
	if proc.Process == nil {
		return bgproto.BgExecResponse{Action: "kill", Err: "already_exited"}
	}
	// Target the process group so children of e.g. "npm run dev" also die.
	if err := syscall.Kill(-proc.Process.Pid, sig); err != nil {
		// On race with natural exit, kill(2) returns ESRCH.
		// Surface as already_exited rather than failing.
		proc.mu.Lock()
		st := proc.state
		proc.mu.Unlock()
		if st != "running" {
			return statusFrom(proc, "kill")
		}
		return bgproto.BgExecResponse{Action: "kill", Err: "kill_failed", ErrDetail: err.Error()}
	}
	proc.mu.Lock()
	proc.state = "killed"
	proc.mu.Unlock()
	return statusFrom(proc, "kill")
}

// List returns a one-line snapshot per registered process.
func (r *BgRegistry) List() bgproto.BgExecResponse {
	r.mu.Lock()
	out := make([]bgproto.BgProcessInfo, 0, len(r.processes))
	for _, p := range r.processes {
		p.mu.Lock()
		info := bgproto.BgProcessInfo{
			ProcessID: p.ID,
			Label:     p.Label,
			Cmd:       p.Cmd,
			State:     p.state,
			Exitcode:  p.exitcode,
			StartedAt: p.StartedAt.Unix(),
		}
		if !p.endedAt.IsZero() {
			info.EndedAt = p.endedAt.Unix()
		}
		p.mu.Unlock()
		out = append(out, info)
	}
	r.mu.Unlock()
	return bgproto.BgExecResponse{Action: "list", Processes: out}
}

func (r *BgRegistry) lookup(id string) (*BgProcess, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.processes[id]
	return p, ok
}

// GC drops finished processes idle longer than r.idleGC. Caller is
// expected to invoke periodically (e.g. ticker every 5 minutes).
func (r *BgRegistry) GC() (removed []string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.processes {
		p.mu.Lock()
		isDone := p.state != "running"
		idle := !p.endedAt.IsZero() && now.Sub(p.endedAt) > r.idleGC
		p.mu.Unlock()
		if isDone && idle {
			delete(r.processes, id)
			removed = append(removed, id)
		}
	}
	return
}

// helper: build env (same shape as tools/exec.go buildEnv). Re-implement
// here to avoid taking dependency on that file's private symbol.
//
// buildEnv is in exec.go; this file uses the same package, so it's already
// in scope.
//
// (keeping this comment as a breadcrumb in case the helper moves.)
var _ = fmt.Sprintf
