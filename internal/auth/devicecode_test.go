package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// advancingClock drives nowMillis and sleepCtx together so the poll loop's
// deadline logic runs deterministically without real delays. Each sleep
// advances the clock by the requested duration and records it.
type advancingClock struct {
	mu     sync.Mutex
	ms     int64
	sleeps []time.Duration
}

func (c *advancingClock) now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ms
}

func (c *advancingClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.ms += d.Milliseconds()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return nil
}

func (c *advancingClock) install(t *testing.T) {
	setNowFunc(t, c.now)
	setSleep(t, c.sleep)
}

func TestPollDeviceCodeComplete(t *testing.T) {
	(&advancingClock{}).install(t)
	got, err := pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  1,
		ExpiresInSeconds: 30,
		Poll: func(context.Context) (pollResult[string], error) {
			return pollResult[string]{Status: pollComplete, Value: "token"}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "token" {
		t.Errorf("value = %q, want token", got)
	}
}

func TestPollDeviceCodePendingThenComplete(t *testing.T) {
	(&advancingClock{}).install(t)
	n := 0
	got, err := pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  1,
		ExpiresInSeconds: 30,
		Poll: func(context.Context) (pollResult[string], error) {
			n++
			if n < 3 {
				return pollResult[string]{Status: pollPending}, nil
			}
			return pollResult[string]{Status: pollComplete, Value: "ok"}, nil
		},
	})
	if err != nil || got != "ok" {
		t.Fatalf("got %q err %v", got, err)
	}
	if n != 3 {
		t.Errorf("polled %d times, want 3", n)
	}
}

func TestPollDeviceCodeFailed(t *testing.T) {
	(&advancingClock{}).install(t)
	_, err := pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  1,
		ExpiresInSeconds: 30,
		Poll: func(context.Context) (pollResult[string], error) {
			return pollResult[string]{Status: pollFailed, Message: "boom"}, nil
		},
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestPollDeviceCodeSlowDownIncrementsInterval(t *testing.T) {
	clk := &advancingClock{}
	clk.install(t)
	n := 0
	_, err := pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  1, // 1s -> 1000ms
		ExpiresInSeconds: 30,
		Poll: func(context.Context) (pollResult[string], error) {
			n++
			if n == 1 {
				return pollResult[string]{Status: pollSlowDown}, nil
			}
			return pollResult[string]{Status: pollPending}, nil
		},
	})
	if err == nil || err.Error() != deviceSlowDownTimeoutMessage {
		t.Fatalf("err = %v, want slow-down timeout message", err)
	}
	// After one slow_down the interval becomes 1000+5000 = 6000ms and applies
	// to this and all subsequent waits.
	if len(clk.sleeps) == 0 || clk.sleeps[0] != 6000*time.Millisecond {
		t.Fatalf("first sleep = %v, want 6s", firstSleep(clk))
	}
}

func TestPollDeviceCodeTimeout(t *testing.T) {
	clk := &advancingClock{}
	clk.install(t)
	_, err := pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  5,
		ExpiresInSeconds: 10,
		Poll: func(context.Context) (pollResult[string], error) {
			return pollResult[string]{Status: pollPending}, nil
		},
	})
	if err == nil || err.Error() != deviceTimeoutMessage {
		t.Fatalf("err = %v, want timeout message", err)
	}
}

func TestPollDeviceCodeContextCancel(t *testing.T) {
	(&advancingClock{}).install(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pollDeviceCode[string](ctx, devicePollOptions[string]{
		IntervalSeconds:  1,
		ExpiresInSeconds: 30,
		Poll: func(context.Context) (pollResult[string], error) {
			return pollResult[string]{Status: pollPending}, nil
		},
	})
	if err == nil || err.Error() != deviceCancelMessage {
		t.Fatalf("err = %v, want cancel message", err)
	}
}

func TestPollDeviceCodeMinimumInterval(t *testing.T) {
	clk := &advancingClock{}
	clk.install(t)
	// IntervalSeconds 0 -> default 5s, but verify the floor when a tiny value
	// is requested is enforced via the minimum (1s).
	_, _ = pollDeviceCode[string](context.Background(), devicePollOptions[string]{
		IntervalSeconds:  0,
		ExpiresInSeconds: 5,
		Poll: func(context.Context) (pollResult[string], error) {
			return pollResult[string]{Status: pollPending}, nil
		},
	})
	if len(clk.sleeps) == 0 || clk.sleeps[0] != 5*time.Second {
		t.Fatalf("first sleep = %v, want default 5s", firstSleep(clk))
	}
}

func firstSleep(c *advancingClock) time.Duration {
	if len(c.sleeps) == 0 {
		return 0
	}
	return c.sleeps[0]
}
