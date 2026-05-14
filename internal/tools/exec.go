// Package tools holds the daemon-side implementations of MCP tools.
// Phase 3 ships only `exec`.
package tools

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

// MaxBytesPerStream caps stdout / stderr per request. Phase 4 will raise to 10 MB.
const MaxBytesPerStream = 1 << 20 // 1 MB

// ErrCwdForbidden is returned when ExecRequest.Cwd is not in the allowlist.
var ErrCwdForbidden = errors.New("tools: cwd not in allowed_exec_cwds")

// OnChunk is called for each non-empty stdout/stderr chunk during exec.
type OnChunk func(execproto.ExecChunk)

// osEnviron is a seam for tests; defaults to os.Environ.
var osEnviron = os.Environ

// Exec runs req under sandbox constraints and streams chunks via onChunk.
// envAllowlist lists environment variable names whose current-process value
// is forwarded to the subprocess; everything else is dropped.
func Exec(ctx context.Context, req execproto.ExecRequest, allowedCwds, envAllowlist []string, onChunk OnChunk) (execproto.ExecComplete, error) {
	if !sandbox.PathAllowed(req.Cwd, allowedCwds) {
		return execproto.ExecComplete{}, ErrCwdForbidden
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, req.Cmd, req.Args...)
	cmd.Dir = req.Cwd
	cmd.Env = buildEnv(envAllowlist, req.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return execproto.ExecComplete{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return execproto.ExecComplete{}, err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return execproto.ExecComplete{}, err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go streamChunks(&wg, stdout, "stdout", onChunk)
	go streamChunks(&wg, stderr, "stderr", onChunk)

	waitErr := cmd.Wait()
	wg.Wait()
	dur := time.Since(start)

	complete := execproto.ExecComplete{
		DurationMs: int(dur / time.Millisecond),
		TimedOut:   cctx.Err() == context.DeadlineExceeded,
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			complete.ExitCode = ee.ExitCode()
		} else {
			complete.ExitCode = -1
		}
	} else {
		complete.ExitCode = 0
	}
	return complete, nil
}

func buildEnv(allow []string, extra map[string]string) []string {
	allowed := make(map[string]struct{}, len(allow))
	for _, k := range allow {
		allowed[k] = struct{}{}
	}
	env := make([]string, 0, len(allow)+len(extra))
	for _, kv := range osEnviron() {
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			if _, ok := allowed[kv[:eq]]; ok {
				env = append(env, kv)
			}
		}
	}
	for k, v := range extra {
		if _, ok := allowed[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

const chunkSize = 64 * 1024

func streamChunks(wg *sync.WaitGroup, r io.Reader, name string, onChunk OnChunk) {
	defer wg.Done()
	buf := make([]byte, chunkSize)
	var total int
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if total+n > MaxBytesPerStream {
				n = MaxBytesPerStream - total
				if n > 0 {
					data := make([]byte, n)
					copy(data, buf[:n])
					onChunk(execproto.ExecChunk{Stream: name, Data: data})
					total += n
				}
				_, _ = io.Copy(io.Discard, r)
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			onChunk(execproto.ExecChunk{Stream: name, Data: data})
			total += n
		}
		if err != nil {
			return
		}
	}
}
