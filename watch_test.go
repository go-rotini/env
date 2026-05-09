package env_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-rotini/env"
)

// pushSource is a tiny test [env.Source] + [env.Watcher] that lets a
// test mutate values and trigger Change events on demand. Models the
// behavior of dotenv/watcher and any other push-based source the
// loader integrates with.
type pushSource struct {
	mu     sync.Mutex
	values map[string]string
	subs   []chan env.Change
	closed bool
}

func newPushSource(initial map[string]string) *pushSource {
	dup := make(map[string]string, len(initial))
	maps.Copy(dup, initial)
	return &pushSource{values: dup}
}

func (p *pushSource) Lookup(key string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.values[key]
	return v, ok
}

func (p *pushSource) Watch(ctx context.Context) (<-chan env.Change, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("pushSource: closed")
	}
	ch := make(chan env.Change, 4)
	p.subs = append(p.subs, ch)
	go func() {
		<-ctx.Done()
		p.mu.Lock()
		defer p.mu.Unlock()
		for i, sub := range p.subs {
			if sub == ch {
				p.subs = slices.Delete(p.subs, i, i+1)
				close(ch)
				break
			}
		}
	}()
	return ch, nil
}

// broadcastLocked sends one env.Change to every active subscriber. Must
// be called with p.mu held — that ordering is what prevents a
// concurrent ctx.Done close goroutine from closing a sub mid-send (the
// close goroutine also takes p.mu).
func (p *pushSource) broadcastLocked(ch env.Change) {
	for _, sub := range p.subs {
		select {
		case sub <- ch:
		default:
		}
	}
}

// set updates one key and broadcasts a Change naming that key.
func (p *pushSource) set(key, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[key] = value
	p.broadcastLocked(env.Change{Keys: []string{key}})
}

// setMany updates multiple keys atomically and broadcasts one Change
// naming the union. Used by concurrency stress tests where every
// generation flips multiple correlated keys.
func (p *pushSource) setMany(values map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(values))
	for k, v := range values {
		p.values[k] = v
		keys = append(keys, k)
	}
	slices.Sort(keys)
	p.broadcastLocked(env.Change{Keys: keys})
}

// del removes one key and broadcasts a Change naming it.
func (p *pushSource) del(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.values, key)
	p.broadcastLocked(env.Change{Keys: []string{key}})
}

// signalEmpty broadcasts a Change with no keys (the "everything may
// have changed" signal).
func (p *pushSource) signalEmpty() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.broadcastLocked(env.Change{})
}

// --- Test helpers ---

// awaitEvent drains one event with a generous timeout. Failing the
// test rather than returning an error keeps call sites tight.
func awaitEvent(t *testing.T, ch <-chan env.Event, msg string) env.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed unexpectedly", msg)
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: timed out waiting for event", msg)
	}
	return env.Event{}
}

// awaitClose returns when ch is closed; fails the test on timeout.
func awaitClose(t *testing.T, ch <-chan env.Event, msg string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("%s: channel did not close", msg)
		}
	}
}

// --- Loader.Watch — initial Load ---

type basicCfg struct {
	Foo string `env:"FOO"`
	Bar string `env:"BAR,default=barDefault"`
}

func TestWatch_InitialLoadSucceeds(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "first"})
	loader := env.New(env.WithSource(src))

	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := loader.Watch(ctx, &cfg)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if cfg.Foo != "first" || cfg.Bar != "barDefault" {
		t.Errorf("after initial Load: cfg = %+v", cfg)
	}

	// No event should fire from the initial Load — events fire only on
	// reload attempts triggered by source changes.
	select {
	case ev := <-ch:
		t.Errorf("unexpected event after initial Load: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatch_InitialLoadFailureReturnsError(t *testing.T) {
	t.Parallel()
	type required struct {
		Token string `env:"TOKEN,required"`
	}
	loader := env.New(env.WithSource(newPushSource(nil)))
	var cfg required

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := loader.Watch(ctx, &cfg)
	if err == nil {
		t.Errorf("expected initial-load error; got channel %v", ch)
	}
}

// --- Reload triggered by Source change ---

func TestWatch_ReloadOnPush(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v1"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := loader.Watch(ctx, &cfg)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	src.set("FOO", "v2")
	ev := awaitEvent(t, ch, "after first push")
	if ev.Err != nil {
		t.Errorf("ev.Err = %v, want nil", ev.Err)
	}
	if !slices.Contains(ev.Changed, "FOO") {
		t.Errorf("ev.Changed = %v, want to include FOO", ev.Changed)
	}
	if cfg.Foo != "v2" {
		t.Errorf("cfg.Foo = %q, want v2", cfg.Foo)
	}

	src.set("FOO", "v3")
	ev = awaitEvent(t, ch, "after second push")
	if cfg.Foo != "v3" {
		t.Errorf("cfg.Foo = %q, want v3", cfg.Foo)
	}
	_ = ev
}

func TestWatch_DebounceCoalesces(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v0"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(120*time.Millisecond),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)

	for i := 1; i <= 5; i++ {
		src.set("FOO", "v"+itoa(i))
		time.Sleep(15 * time.Millisecond)
	}

	ev := awaitEvent(t, ch, "coalesced reload")
	if ev.Err != nil {
		t.Fatalf("ev.Err = %v", ev.Err)
	}
	if cfg.Foo != "v5" {
		t.Errorf("cfg.Foo = %q, want v5", cfg.Foo)
	}

	// No second event should fire.
	select {
	case ev := <-ch:
		t.Errorf("got second event %+v; debounce should have coalesced the burst", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatch_EmptyKeyChange_TriggersReload(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "init"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)

	src.set("FOO", "changed")
	src.signalEmpty()
	awaitEvent(t, ch, "from empty-key signal")

	if cfg.Foo != "changed" {
		t.Errorf("cfg.Foo = %q, want changed", cfg.Foo)
	}
}

func TestWatch_NoDebounce_FiresImmediately(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "a"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(0),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)

	src.set("FOO", "b")
	awaitEvent(t, ch, "first")
	src.set("FOO", "c")
	awaitEvent(t, ch, "second")
	if cfg.Foo != "c" {
		t.Errorf("cfg.Foo = %q, want c", cfg.Foo)
	}
}

// --- Reload retains old values on failure ---

func TestWatch_ReloadFailureRetainsValue(t *testing.T) {
	t.Parallel()
	type required struct {
		Token string `env:"TOKEN,required"`
	}
	src := newPushSource(map[string]string{"TOKEN": "ok"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg required
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := loader.Watch(ctx, &cfg)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Removing the required key triggers a reload that fails.
	src.del("TOKEN")
	ev := awaitEvent(t, ch, "after del")
	if ev.Err == nil {
		t.Fatalf("expected reload error; got nil with cfg.Token=%q", cfg.Token)
	}
	if !errors.Is(ev.Err, &env.MissingRequiredError{}) {
		t.Errorf("ev.Err = %v, want MissingRequiredError", ev.Err)
	}
	if cfg.Token != "ok" {
		t.Errorf("cfg.Token = %q, want \"ok\" (failed reload should retain old value)", cfg.Token)
	}
}

// --- Immutable-field guard ---

type immutableCfg struct {
	Region string `env:"REGION,immutable"`
	Other  string `env:"OTHER"`
}

func TestWatch_ImmutableChangeRejects(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"REGION": "us-east", "OTHER": "x"})
	loader := env.New(
		env.WithSource(src),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg immutableCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)

	src.set("OTHER", "y") // OK — not immutable.
	ev := awaitEvent(t, ch, "OTHER change")
	if ev.Err != nil {
		t.Fatalf("ev.Err = %v, want nil", ev.Err)
	}
	if cfg.Other != "y" {
		t.Errorf("cfg.Other = %q, want y", cfg.Other)
	}

	src.set("REGION", "us-west") // not OK — immutable.
	ev = awaitEvent(t, ch, "REGION change")
	if !errors.Is(ev.Err, &env.ImmutableChangedError{}) {
		t.Errorf("ev.Err = %v, want ImmutableChangedError", ev.Err)
	}
	// Old value must be retained.
	if cfg.Region != "us-east" {
		t.Errorf("cfg.Region = %q, want us-east (immutable rollback)", cfg.Region)
	}
}

// --- Polling for non-Watcher sources ---

func TestWatch_PollingNonWatcherSource(t *testing.T) {
	t.Parallel()
	mu := &sync.Mutex{}
	store := map[string]string{"FOO": "init"}
	getter := func(key string) (string, bool) {
		mu.Lock()
		defer mu.Unlock()
		v, ok := store[key]
		return v, ok
	}
	src := pollOnlySource{lookup: getter}

	loader := env.New(
		env.WithSource(src),
		env.WithPollInterval(40*time.Millisecond),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)
	// Initial Load completes before Watch returns — safe to read.
	if cfg.Foo != "init" {
		t.Fatalf("initial: cfg.Foo = %q", cfg.Foo)
	}

	mu.Lock()
	store["FOO"] = "polled"
	mu.Unlock()

	// Read cfg ONLY after an event arrives. The channel send establishes
	// happens-before with the worker's reflect-write into &cfg.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-ch:
			if cfg.Foo == "polled" {
				return
			}
		case <-deadline:
			t.Fatalf("after polling: cfg.Foo = %q, want polled", cfg.Foo)
		}
	}
}

// pollOnlySource implements env.Source but NOT env.Watcher; used to
// verify the poll-loop path.
type pollOnlySource struct {
	lookup func(string) (string, bool)
}

func (p pollOnlySource) Lookup(key string) (string, bool) { return p.lookup(key) }

// --- Context cancel closes the events channel ---

func TestWatch_CtxCancelClosesChannel(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "v"})
	loader := env.New(env.WithSource(src), env.WithReloadDebounce(10*time.Millisecond))
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	ch, _ := loader.Watch(ctx, &cfg)

	cancel()
	awaitClose(t, ch, "after ctx cancel")
}

// --- Multiple Watcher sources fan in ---

func TestWatch_MultipleWatcherSources(t *testing.T) {
	t.Parallel()
	src1 := newPushSource(map[string]string{"FOO": "a"})
	src2 := newPushSource(map[string]string{"BAR": "b"})
	loader := env.New(
		env.WithSource(src1, src2),
		env.WithReloadDebounce(20*time.Millisecond),
	)
	var cfg basicCfg
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := loader.Watch(ctx, &cfg)

	src1.set("FOO", "a2")
	awaitEvent(t, ch, "src1 change")
	src2.set("BAR", "b2")
	awaitEvent(t, ch, "src2 change")
	if cfg.Foo != "a2" || cfg.Bar != "b2" {
		t.Errorf("cfg = %+v", cfg)
	}
}

// --- Watch on missing pieces ---

func TestWatch_NilContext(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "x"})
	loader := env.New(env.WithSource(src))
	var cfg basicCfg
	var nilCtx context.Context // intentional: exercises the nil-ctx guard
	_, err := loader.Watch(nilCtx, &cfg)
	if !errors.Is(err, env.ErrNilContext) {
		t.Errorf("err = %v, want ErrNilContext", err)
	}
}

// --- itoa (avoid strconv) ---

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
