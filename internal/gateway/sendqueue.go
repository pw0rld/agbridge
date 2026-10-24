package gateway

import (
	"context"
	"errors"
	"sync"

	"github.com/pw0rld/agbridge/internal/proto"
)

// Bounds for the daemon→bridge fan-out queue. Two limits because
// frames legitimately reach 64 MiB (proto.maxPayloadLen): a
// frame-count bound alone could still buffer tens of gigabytes.
const (
	bridgeQueueDepth    = 256
	bridgeQueueMaxBytes = 32 << 20
)

// ErrSendQueueFull is returned by frameQueue.submit when a bridge is
// not draining as fast as the daemon produces. The caller treats it
// as a dead session and tears the bridge conn down instead of
// dropping frames — dropped frames would permanently corrupt E2E
// nonce ordering and break stream/frame demux at the receiver.
var ErrSendQueueFull = errors.New("gateway: bridge send queue full")

// frameQueue decouples the daemon read loop (which fans frames out
// through DaemonProxy.Deliver) from one bridge's WSS writes: submit is
// non-blocking and a dedicated sender goroutine serializes frames onto
// the conn. Without it, one stalled bridge (TCP backpressure) blocks
// the daemon's entire fan-out — head-of-line blocking every other
// subscriber of that daemon, plus the daemon's own control traffic.
type frameQueue struct {
	ch     chan proto.Frame
	ctx    context.Context // cancel stops the in-flight send and exits the sender
	sendFn func(context.Context, proto.Frame) error

	mu    sync.Mutex
	bytes int // payload bytes queued (and, briefly, in flight)
}

// newFrameQueue starts the sender goroutine; it exits when ctx is
// cancelled or a send fails (the conn is dead either way and the
// owning handleBridge tears the session down).
func newFrameQueue(ctx context.Context, send func(context.Context, proto.Frame) error) *frameQueue {
	q := &frameQueue{
		ch:     make(chan proto.Frame, bridgeQueueDepth),
		ctx:    ctx,
		sendFn: send,
	}
	go q.run()
	return q
}

// submit enqueues f without blocking. It returns ErrSendQueueFull
// when the queue is full, cancelled, or the sender is gone; Deliver
// turns any error into an Unsubscribe, and the handleBridge wrapper
// additionally severs the bridge conn on overflow.
func (q *frameQueue) submit(f proto.Frame) error {
	if err := q.ctx.Err(); err != nil {
		return err
	}
	n := len(f.Payload)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+n > bridgeQueueMaxBytes || len(q.ch) == cap(q.ch) {
		return ErrSendQueueFull
	}
	q.bytes += n
	q.ch <- f // non-blocking: len < cap verified under the same lock
	return nil
}

func (q *frameQueue) run() {
	for {
		select {
		case <-q.ctx.Done():
			return
		case f := <-q.ch:
			if err := q.sendFn(q.ctx, f); err != nil {
				return
			}
			q.mu.Lock()
			q.bytes -= len(f.Payload)
			q.mu.Unlock()
		}
	}
}
