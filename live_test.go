package env_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-rotini/env"
)

// liveCfg is the canonical Live[T] target shape used across tests.
type liveCfg struct {
	Foo  string `env:"FOO,default=fooDefault"`
	Bar  int    `env:"BAR,default=42"`
	Tier string `env:"TIER,immutable,default=prod"`
}

// --- NewLive lifecycle ---

func TestNewLive_InitialLoadSucceeds(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "first"})
	live, err := env.NewLive[liveCfg](env.WithSource(src))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	cfg := live.Get()
	if cfg == nil {
		t.Fatal("Get returned nil")
	}
	if cfg.Foo != "first" {
		t.Errorf("Foo = %q, want first", cfg.Foo)
	}
	if cfg.Bar != 42 {
		t.Errorf("Bar = %d, want 42", cfg.Bar)
	}
	if cfg.Tier != "prod" {
		t.Errorf("Tier = %q, want prod", cfg.Tier)
	}
}

func TestNewLive_InitialLoadFailureReturnsError(t *testing.T) {
	t.Parallel()
	type required struct {
		Token string `env:"TOKEN,required"`
	}
	live, err := env.NewLive[required](env.WithSource(newPushSource(nil)))
	if err == nil {
		t.Errorf("expected error; got live=%v", live)
	}
	if live != nil {
		t.Error("Live must be nil when initial Load fails")
	}
}

func TestNewLiveWithLoader_NilLoader(t *testing.T) {
	t.Parallel()
	live, err := env.NewLiveWithLoader[liveCfg](nil)
	if err == nil {
		t.Errorf("expected error for nil loader; got live=%v", live)
	}
}

// --- atomic-pointer swap ---

func TestLive_GetReturnsLatestAfterReload(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v1"})
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	if live.Get().Foo != "v1" {
		t.Fatalf("initial Foo = %q", live.Get().Foo)
	}

	src.set("FOO", "v2")
	awaitEvent(t, live.Events(), "after first push")
	if live.Get().Foo != "v2" {
		t.Errorf("Get().Foo = %q, want v2", live.Get().Foo)
	}

	src.set("FOO", "v3")
	awaitEvent(t, live.Events(), "after second push")
	if live.Get().Foo != "v3" {
		t.Errorf("Get().Foo = %q, want v3", live.Get().Foo)
	}
}

// TestLive_GetSnapshotIsImmutable proves the documented semantics: the
// pointer returned by an earlier Get continues to reference the value
// it observed, even after the worker installs a newer pointer.
func TestLive_GetSnapshotIsImmutable(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "before"})
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	old := live.Get()
	src.set("FOO", "after")
	awaitEvent(t, live.Events(), "after reload")

	curr := live.Get()
	if old.Foo != "before" {
		t.Errorf("old snapshot mutated: Foo = %q, want before", old.Foo)
	}
	if curr.Foo != "after" {
		t.Errorf("new snapshot Foo = %q, want after", curr.Foo)
	}
	if old == curr {
		t.Error("expected distinct pointers after reload")
	}
}

// --- failed reload retains current ---

func TestLive_FailedReloadRetainsCurrent(t *testing.T) {
	t.Parallel()
	type required struct {
		Token string `env:"TOKEN,required"`
		Other string `env:"OTHER,default=fallback"`
	}
	src := newPushSource(map[string]string{"TOKEN": "secret-1"})
	live, err := env.NewLive[required](
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	old := live.Get()
	src.del("TOKEN") // makes the next reload fail.
	ev := awaitEvent(t, live.Events(), "after del")
	if ev.Err == nil {
		t.Fatalf("expected reload error; got nil")
	}
	if live.Get() != old {
		t.Error("Get returned a new pointer after a failed reload (should retain old)")
	}
	if live.Get().Token != "secret-1" {
		t.Errorf("Token = %q, want secret-1", live.Get().Token)
	}
}

// --- immutable rejection ---

func TestLive_ImmutableChangeRejected(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"TIER": "prod"})
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	old := live.Get()
	src.set("TIER", "staging") // immutable — must reject.
	ev := awaitEvent(t, live.Events(), "after immutable change")
	if !errors.Is(ev.Err, &env.ImmutableChangedError{}) {
		t.Errorf("ev.Err = %v, want ImmutableChangedError", ev.Err)
	}
	if live.Get() != old {
		t.Error("Live installed a new pointer despite immutable rejection")
	}
}

// --- manual Reload ---

func TestLive_ReloadIsSynchronous(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "x"})
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithReloadDebounce(500*time.Millisecond), // long, to prove Reload bypasses it.
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	// Mutate the source WITHOUT signaling — the watcher would normally
	// not notice (no change event sent). Then Reload(): the manual
	// call must trigger a fresh load that picks up the new value.
	src.mu.Lock()
	src.values["FOO"] = "manual"
	src.mu.Unlock()

	if err := live.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if live.Get().Foo != "manual" {
		t.Errorf("after Reload: Foo = %q, want manual", live.Get().Foo)
	}

	// One Event must have fired for the manual reload.
	awaitEvent(t, live.Events(), "manual reload event")
}

func TestLive_ReloadReturnsErrorOnFailure(t *testing.T) {
	t.Parallel()
	type required struct {
		Token string `env:"TOKEN,required"`
	}
	src := newPushSource(map[string]string{"TOKEN": "ok"})
	live, err := env.NewLive[required](
		env.WithSource(src),
		env.WithReloadDebounce(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	src.mu.Lock()
	delete(src.values, "TOKEN")
	src.mu.Unlock()

	err = live.Reload()
	if err == nil {
		t.Fatal("Reload should return an error when the load fails")
	}
	if !errors.Is(err, &env.MissingRequiredError{}) {
		t.Errorf("err = %v, want MissingRequiredError", err)
	}
	awaitEvent(t, live.Events(), "manual reload event (failed)")
}

func TestLive_ReloadAfterCloseErrors(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v"})
	live, err := env.NewLive[liveCfg](env.WithSource(src))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := live.Reload(); !errors.Is(err, env.ErrWatcherClosed) {
		t.Errorf("Reload after Close: err = %v, want ErrWatcherClosed", err)
	}
}

// --- Events channel semantics ---

func TestLive_EventsChannelClosesOnClose(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v"})
	live, err := env.NewLive[liveCfg](env.WithSource(src))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	ch := live.Events()
	if err := live.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	awaitClose(t, ch, "Events channel after Close")
}

func TestLive_EventsChannelDeliversChanged(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "1"})
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	src.set("FOO", "2")
	ev := awaitEvent(t, live.Events(), "after change")
	if ev.Err != nil {
		t.Errorf("ev.Err = %v", ev.Err)
	}
	if !slices.Contains(ev.Changed, "FOO") {
		t.Errorf("ev.Changed = %v, want to include FOO", ev.Changed)
	}
	if ev.Time.IsZero() {
		t.Error("ev.Time is zero")
	}
}

// --- Close idempotence ---

func TestLive_CloseIdempotent(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v"})
	live, err := env.NewLive[liveCfg](env.WithSource(src))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// --- Get continues to work after Close ---

func TestLive_GetAfterClose(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "after-close"})
	live, err := env.NewLive[liveCfg](env.WithSource(src))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	if err := live.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	cfg := live.Get()
	if cfg == nil || cfg.Foo != "after-close" {
		t.Errorf("Get after Close = %+v", cfg)
	}
}

// --- pollInterval drives Live reloads ---

func TestLive_PollDrivenReload(t *testing.T) {
	t.Parallel()
	mu := &sync.Mutex{}
	store := map[string]string{"FOO": "polled-init"}
	src := pollOnlySource{
		lookup: func(key string) (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			v, ok := store[key]
			return v, ok
		},
	}
	live, err := env.NewLive[liveCfg](
		env.WithSource(src),
		env.WithPollInterval(40*time.Millisecond),
		env.WithReloadDebounce(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	if live.Get().Foo != "polled-init" {
		t.Fatalf("initial Foo = %q", live.Get().Foo)
	}

	mu.Lock()
	store["FOO"] = "polled-after"
	mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for live.Get().Foo != "polled-after" && time.Now().Before(deadline) {
		select {
		case <-live.Events():
		case <-time.After(50 * time.Millisecond):
		}
	}
	if live.Get().Foo != "polled-after" {
		t.Errorf("after polling: Foo = %q, want polled-after", live.Get().Foo)
	}
}
