package tools

import (
	"context"
	"testing"
	"time"
)

func TestBgRegistryGCLoop(t *testing.T) {
	r := &BgRegistry{
		processes: make(map[string]*BgProcess),
		idleGC:    10 * time.Millisecond,
	}
	dead := &BgProcess{ID: "p_dead", StartedAt: time.Now().Add(-time.Hour)}
	dead.state = "exited"
	dead.endedAt = time.Now().Add(-time.Minute)
	r.processes[dead.ID] = dead

	live := &BgProcess{ID: "p_live", StartedAt: time.Now()}
	live.state = "running"
	r.processes[live.ID] = live

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.GCLoop(ctx, 10*time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		_, stillThere := r.processes["p_dead"]
		_, liveThere := r.processes["p_live"]
		r.mu.Unlock()
		if liveThere && !stillThere {
			break
		}
		if !liveThere {
			t.Fatal("GCLoop collected a running process")
		}
		if time.Now().After(deadline) {
			t.Fatal("GCLoop did not collect the idle finished process")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GCLoop ignores ctx cancellation")
	}
}
