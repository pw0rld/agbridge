package dialer

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoopFirstAttemptSuccess(t *testing.T) {
	var calls int
	err := Loop(context.Background(), func(ctx context.Context) error {
		calls++
		return nil
	}, Options{Base: time.Millisecond, Cap: 10 * time.Millisecond, Jitter: 0, sleep: noSleep})
	if err != nil {
		t.Fatalf("Loop returned %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestLoopRetriesUntilSuccess(t *testing.T) {
	var calls int
	want := errors.New("transient")
	err := Loop(context.Background(), func(ctx context.Context) error {
		calls++
		if calls < 4 {
			return want
		}
		return nil
	}, Options{Base: time.Millisecond, Cap: 10 * time.Millisecond, Jitter: 0, sleep: noSleep})
	if err != nil {
		t.Fatalf("Loop returned %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls = %d, want 4", calls)
	}
}

func TestLoopBackoffGrowsExponentially(t *testing.T) {
	var calls int
	var delays []time.Duration
	_ = Loop(context.Background(), func(ctx context.Context) error {
		calls++
		if calls < 5 {
			return errors.New("nope")
		}
		return nil
	}, Options{Base: 100 * time.Millisecond, Cap: 5 * time.Second, Jitter: 0,
		sleep: func(d time.Duration) { delays = append(delays, d) }})
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	if len(delays) != len(want) {
		t.Fatalf("delays len = %d, want %d (%v)", len(delays), len(want), delays)
	}
	for i, d := range want {
		if delays[i] != d {
			t.Errorf("delays[%d] = %v, want %v", i, delays[i], d)
		}
	}
}

func TestLoopRespectsCap(t *testing.T) {
	var delays []time.Duration
	_ = Loop(context.Background(), func(ctx context.Context) error {
		if len(delays) >= 6 {
			return nil
		}
		return errors.New("nope")
	}, Options{Base: 100 * time.Millisecond, Cap: 300 * time.Millisecond, Jitter: 0,
		sleep: func(d time.Duration) { delays = append(delays, d) }})
	for i, d := range delays {
		if d > 300*time.Millisecond {
			t.Errorf("delays[%d] = %v, exceeds cap 300ms", i, d)
		}
	}
	if delays[len(delays)-1] != 300*time.Millisecond {
		t.Errorf("last delay = %v, expected to have saturated at 300ms", delays[len(delays)-1])
	}
}

func TestLoopJitterStaysInBand(t *testing.T) {
	const base = 1000 * time.Millisecond
	var delays []time.Duration
	_ = Loop(context.Background(), func(ctx context.Context) error {
		if len(delays) >= 20 {
			return nil
		}
		return errors.New("nope")
	}, Options{Base: base, Cap: base, Jitter: 0.2,
		sleep: func(d time.Duration) { delays = append(delays, d) }})
	low := time.Duration(float64(base) * 0.8)
	high := time.Duration(float64(base) * 1.2)
	for i, d := range delays {
		if d < low || d > high {
			t.Errorf("delays[%d] = %v, expected within [%v, %v]", i, d, low, high)
		}
	}
}

func TestLoopContextCancelExits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	err := Loop(ctx, func(ctx context.Context) error {
		calls++
		return errors.New("dont-matter")
	}, Options{Base: time.Hour, Cap: time.Hour, sleep: noSleep})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls > 1 {
		t.Errorf("calls = %d, want ≤1", calls)
	}
}

func TestLoopContextCancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	done := make(chan error, 1)
	go func() {
		done <- Loop(ctx, func(ctx context.Context) error {
			calls++
			return errors.New("nope")
		}, Options{Base: time.Hour, Cap: time.Hour, Jitter: 0})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("Loop did not exit after ctx cancel")
	}
}

func noSleep(d time.Duration) {}
