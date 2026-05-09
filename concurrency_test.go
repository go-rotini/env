package env_test

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rotini/env"
)

// stressCfg is the target shape for concurrency tests. The string
// fields all flow from a single integer counter so that any "torn read"
// (a Get returning a struct whose fields came from different reload
// generations) is detectable: a valid snapshot has Foo == Bar == Baz.
type stressCfg struct {
	Foo string `env:"FOO"`
	Bar string `env:"BAR"`
	Baz string `env:"BAZ"`
}

// TestConcurrency_GetIsTornReadFree spins up many readers calling
// Live.Get in tight loops while a writer publishes new generations.
// The atomic.Pointer.Store + atomic.Pointer.Load contract guarantees
// that every Get returns a *T that was wholly Stored — never a
// half-constructed value where some fields are from one reload and
// other fields are from another.
//
// The writer waits for each reload's Event before publishing the next
// generation. This isolates the test to Live's atomic-pointer
// semantics; broader source-coherency concerns (the engine's per-key
// Lookup calls during a LoadContext) are out of scope, since real-
// world sources are typically stable for the duration of a Load.
func TestConcurrency_GetIsTornReadFree(t *testing.T) {
	t.Parallel()

	src := newPushSource(map[string]string{
		"FOO": "g0",
		"BAR": "g0",
		"BAZ": "g0",
	})
	live, err := env.NewLive[stressCfg](
		env.WithSource(src),
		env.WithReloadDebounce(0),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	const (
		readerCount = 8
		writerGens  = 80
	)

	var (
		stop     atomic.Bool
		readerWG sync.WaitGroup
		torn     atomic.Int64
	)
	for range readerCount {
		readerWG.Go(func() {
			for !stop.Load() {
				cfg := live.Get()
				if cfg.Foo != cfg.Bar || cfg.Bar != cfg.Baz {
					torn.Add(1)
				}
			}
		})
	}

	for i := 1; i <= writerGens; i++ {
		gen := "g" + itoa(i)
		src.setMany(map[string]string{"FOO": gen, "BAR": gen, "BAZ": gen})
		// Wait for the engine to finish this reload before advancing.
		select {
		case <-live.Events():
		case <-time.After(2 * time.Second):
			t.Fatalf("reload event for %s did not arrive", gen)
		}
	}

	stop.Store(true)
	readerWG.Wait()

	if got := torn.Load(); got != 0 {
		t.Errorf("observed %d torn reads; expected 0", got)
	}
}

// TestConcurrency_ManyReloadsThenGet verifies that under heavy reload
// pressure with parallel reads, `Live.Get` always returns a non-nil
// pointer (the worker never temporarily clears the atomic.Pointer).
func TestConcurrency_GetNeverNil(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "init", "BAR": "init", "BAZ": "init"})
	live, err := env.NewLive[stressCfg](
		env.WithSource(src),
		env.WithReloadDebounce(0),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	var (
		stop     atomic.Bool
		readerWG sync.WaitGroup
		nilSeen  atomic.Int64
	)
	for range 16 {
		readerWG.Go(func() {
			for !stop.Load() {
				if live.Get() == nil {
					nilSeen.Add(1)
				}
			}
		})
	}

	for i := range 100 {
		gen := "v" + itoa(i)
		src.setMany(map[string]string{"FOO": gen, "BAR": gen, "BAZ": gen})
	}
	time.Sleep(20 * time.Millisecond)
	stop.Store(true)
	readerWG.Wait()

	if nilSeen.Load() != 0 {
		t.Errorf("Get returned nil %d times during reload storm", nilSeen.Load())
	}
}

// TestConcurrency_ReloadWhileClose exercises the close path under
// concurrent activity: while the writer is generating events, Close
// is invoked. The worker must drain cleanly, the events channel must
// close, and no goroutine may leak.
func TestConcurrency_ReloadWhileClose(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v0", "BAR": "v0", "BAZ": "v0"})
	live, err := env.NewLive[stressCfg](
		env.WithSource(src),
		env.WithReloadDebounce(0),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}

	var stopWriter atomic.Bool
	var writerWG sync.WaitGroup
	writerWG.Go(func() {
		i := 0
		for !stopWriter.Load() {
			i++
			gen := "g" + itoa(i)
			src.setMany(map[string]string{"FOO": gen, "BAR": gen, "BAZ": gen})
		}
	})

	// Read while events fly.
	var readerWG sync.WaitGroup
	readerWG.Go(func() {
		for ev := range live.Events() {
			_ = ev
		}
	})

	// Let some events accumulate.
	time.Sleep(40 * time.Millisecond)

	if err := live.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	stopWriter.Store(true)
	writerWG.Wait()
	readerWG.Wait() // exits when Events() closes.

	// Get continues to work post-close.
	if live.Get() == nil {
		t.Error("Get after Close returned nil")
	}
}

// TestConcurrency_ManualReloadUnderEventStorm checks that synchronous
// Reload returns even when the worker is being hammered by source-
// change events. The worker serializes through a single goroutine, so
// Reload must always make progress.
func TestConcurrency_ManualReloadUnderEventStorm(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "0", "BAR": "0", "BAZ": "0"})
	live, err := env.NewLive[stressCfg](
		env.WithSource(src),
		env.WithReloadDebounce(0),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	// Drain events in the background.
	go func() {
		for range live.Events() {
		}
	}()

	var stopWriter atomic.Bool
	var writerWG sync.WaitGroup
	writerWG.Go(func() {
		i := 0
		for !stopWriter.Load() {
			i++
			gen := "v" + itoa(i)
			src.mu.Lock()
			src.values["FOO"] = gen
			src.values["BAR"] = gen
			src.values["BAZ"] = gen
			subs := slices.Clone(src.subs)
			src.mu.Unlock()
			for _, sub := range subs {
				select {
				case sub <- env.Change{Keys: []string{"FOO"}}:
				default:
				}
			}
		}
	})

	for i := range 10 {
		if err := live.Reload(); err != nil {
			t.Errorf("Reload #%d: %v", i, err)
		}
	}
	stopWriter.Store(true)
	writerWG.Wait()
}
