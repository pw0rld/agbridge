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

// ErrSendQueueClosed is returned by frameQueue.submit after the
// sender goroutine has shut down (ctx cancelled or conn died).
// Deliver turns it into an Unsubscribe, so a racing producer simply
// detaches; nothing is enqueued behind a queue that will never drain.
var ErrSendQueueClosed = errors.New("gateway: bridge send queue closed")

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
	m      *Metrics // nil-safe occupancy gauges; nil disables metrics

	mu     sync.Mutex
	bytes  int  // payload bytes queued (and, briefly, in flight)
	closed bool // set by run() before its final drain; rejects late submits
}

// newFrameQueue starts the sender goroutine; it exits when ctx is
// cancelled or a send fails (the conn is dead either way and the
// owning handleBridge tears the session down). m records queue
// occupancy gauges and may be nil.
func newFrameQueue(ctx context.Context, send func(context.Context, proto.Frame) error, m *Metrics) *frameQueue {
	q := &frameQueue{
		ch:     make(chan proto.Frame, bridgeQueueDepth),
		ctx:    ctx,
		sendFn: send,
		m:      m,
	}
	go q.run()
	return q
}

// submit enqueues f without blocking. It returns ErrSendQueueFull
// when the queue is full, ErrSendQueueClosed (or the ctx error) once
// the sender is gone; Deliver turns any error into an Unsubscribe, and
// the handleBridge wrapper additionally severs the bridge conn on
// overflow.
func (q *frameQueue) submit(f proto.Frame) error {
	if err := q.ctx.Err(); err != nil {
		return err
	}
	n := len(f.Payload)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrSendQueueClosed
	}
	if q.bytes+n > bridgeQueueMaxBytes || len(q.ch) == cap(q.ch) {
		return ErrSendQueueFull
	}
	q.bytes += n
	q.ch <- f // non-blocking: len < cap verified under the same lock
	q.m.noteQueueIn(n)
	return nil
}

func (q *frameQueue) run() {
	dead := false // a failed send means the conn is gone: stop trying, keep draining
	for {
		select {
		case <-q.ctx.Done():
			q.closeAndDrain()
			return
		case f := <-q.ch:
			if dead {
				q.release(f)
				continue
			}
			if err := q.sendFn(q.ctx, f); err != nil {
				dead = true
				q.release(f)
				continue
			}
			q.release(f)
			q.m.noteToBridge(len(f.Payload))
		}
	}
}

// release accounts for one frame leaving the queue without a
// successful send. Occupancy gauges drop here regardless of outcome,
// so queueFrames/queueBytes can only ever track live queue contents.
func (q *frameQueue) release(f proto.Frame) {
	q.mu.Lock()
	q.bytes -= len(f.Payload)
	q.mu.Unlock()
	q.m.noteQueueOut(len(f.Payload))
}

// closeAndDrain marks the queue closed (atomically w.r.t. submit's
// bound checks under mu) and empties whatever is left. Marking under
// the same lock submit holds makes the gauge accounting provably
// race-free: after closeAndDrain returns, no submit can have enqueued
// a frame that the drain missed.
func (q *frameQueue) closeAndDrain() {
	q.mu.Lock()
	q.closed = true
	for {
		select {
		case f := <-q.ch:
			q.bytes -= len(f.Payload)
			q.m.noteQueueOut(len(f.Payload))
			continue
		default:
			q.mu.Unlock()
			return
		}
	}
}
