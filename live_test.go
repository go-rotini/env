package env_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestLive_ImmutableSecretRedacted(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"SECRET_TIER": "alpha-1"})
	type cfg struct {
		Tier string `env:"SECRET_TIER,immutable,secret"`
	}
	live, err := env.NewLive[cfg](env.WithSource(src), env.WithReloadDebounce(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	src.set("SECRET_TIER", "beta-22") // immutable secret — must reject, without leaking values.
	ev := awaitEvent(t, live.Events(), "after immutable-secret change")
	var ice *env.ImmutableChangedError
	if !errors.As(ev.Err, &ice) {
		t.Fatalf("ev.Err = %v, want *ImmutableChangedError", ev.Err)
	}
	for _, secret := range []string{"alpha-1", "beta-22"} {
		if strings.Contains(ice.Old, secret) || strings.Contains(ice.New, secret) || strings.Contains(ev.Err.Error(), secret) {
			t.Errorf("secret value %q leaked: Old=%q New=%q msg=%q", secret, ice.Old, ice.New, ev.Err.Error())
		}
	}
	if ice.Old == "" || ice.New == "" {
		t.Errorf("Old/New should be redaction markers, got Old=%q New=%q", ice.Old, ice.New)
	}
	if ice.Field != "Tier" {
		t.Errorf("Field = %q, want Tier", ice.Field)
	}
}

// TestLive_ImmutableExpandChangeRejected proves an `immutable,expand`
// field is compared on its post-expansion value: a change in a
// referenced variable counts as a change even though the field's own
// raw value (the "${HOST}/db" template) never moves.
func TestLive_ImmutableExpandChangeRejected(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"HOST": "h1", "DSN": "${HOST}/db"})
	type cfg struct {
		DSN string `env:"DSN,immutable,expand"`
	}
	live, err := env.NewLive[cfg](env.WithSource(src), env.WithReloadDebounce(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	if live.Get().DSN != "h1/db" {
		t.Fatalf("initial DSN = %q, want h1/db", live.Get().DSN)
	}
	old := live.Get()

	src.set("HOST", "h2") // DSN's template is unchanged, but it now expands to h2/db.
	ev := awaitEvent(t, live.Events(), "after referenced-var change")
	var ice *env.ImmutableChangedError
	if !errors.As(ev.Err, &ice) {
		t.Fatalf("ev.Err = %v, want *ImmutableChangedError", ev.Err)
	}
	// An expanded value can carry other variables' values, so it is
	// redacted.
	if ice.Old != "<redacted: 5 bytes>" || ice.New != "<redacted: 5 bytes>" {
		t.Errorf("ImmutableChangedError Old/New = %q/%q, want redacted", ice.Old, ice.New)
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

// --- reload correctness ---

// lockedMap is a non-Watcher source whose values a test can change.
type lockedMap struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *lockedMap) Lookup(k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

func (s *lockedMap) set(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
}

// flipSource answers "v2" for KEY once after being armed, then "v1":
// a source that changes between two reads within one reload.
type flipSource struct{ armed atomic.Bool }

func (s *flipSource) Lookup(k string) (string, bool) {
	if k != "KEY" {
		return "", false
	}
	if s.armed.CompareAndSwap(true, false) {
		return "v2", true
	}
	return "v1", true
}

func TestLive_ImmutableChecksTheLoadedValue(t *testing.T) {
	t.Parallel()
	type C struct {
		Key string `env:"KEY,immutable"`
	}
	src := &flipSource{}
	live, err := env.NewLive[C](env.WithSource(src))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	src.armed.Store(true)
	if err := live.Reload(); !errors.Is(err, env.ErrImmutableChanged) {
		t.Fatalf("Reload = %v, want ErrImmutableChanged", err)
	}
	if got := live.Get().Key; got != "v1" {
		t.Errorf("Key = %q, want v1", got)
	}
}

func TestLive_ImmutableSeesMutatorResult(t *testing.T) {
	t.Parallel()
	type C struct {
		Key string `env:"KEY,immutable"`
	}
	var n atomic.Int32
	m := env.MutatorFunc(func(_ context.Context, _, _, _, cur string, _ bool) (string, bool, error) {
		return cur + "-" + strconv.Itoa(int(n.Add(1))), false, nil
	})
	live, err := env.NewLive[C](env.WithSource(env.Map(map[string]string{"KEY": "ref"})), env.WithMutator(m))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	if err := live.Reload(); !errors.Is(err, env.ErrImmutableChanged) {
		t.Fatalf("Reload = %v, want ErrImmutableChanged", err)
	}
	if got := live.Get().Key; got != "ref-1" {
		t.Errorf("Key = %q, want ref-1", got)
	}
}

func TestLive_ImmutableSecretTypeRedacted(t *testing.T) {
	t.Parallel()
	type C struct {
		Key env.Secret[string] `env:"KEY,immutable"`
	}
	src := &lockedMap{m: map[string]string{"KEY": "oldsecret"}}
	live, err := env.NewLive[C](env.WithSource(src))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	src.set("KEY", "newsecret")
	ic, ok := errors.AsType[*env.ImmutableChangedError](live.Reload())
	if !ok {
		t.Fatal("want *ImmutableChangedError")
	}
	if strings.Contains(ic.Old+ic.New, "secret") {
		t.Errorf("Old=%q New=%q leak the value", ic.Old, ic.New)
	}
}

func TestLive_UnsetFieldKeptAcrossReloads(t *testing.T) {
	type C struct {
		Token string `env:"ENV_TEST_UNSET_TOKEN,unset"`
		Other string `env:"ENV_TEST_UNSET_OTHER"`
	}
	t.Setenv("ENV_TEST_UNSET_TOKEN", "abc")
	t.Setenv("ENV_TEST_UNSET_OTHER", "1")
	live, err := env.NewLive[C]()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	if _, ok := os.LookupEnv("ENV_TEST_UNSET_TOKEN"); ok {
		t.Fatal("unset-tagged variable still in the environment")
	}
	t.Setenv("ENV_TEST_UNSET_OTHER", "2")
	if err := live.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := live.Get(); got.Token != "abc" || got.Other != "2" {
		t.Errorf("after reload: %+v, want Token=abc Other=2", *got)
	}
}

func TestLive_PollAtOrBelowDebounceReloads(t *testing.T) {
	t.Parallel()
	type C struct {
		V string `env:"V"`
	}
	src := &lockedMap{m: map[string]string{"V": "a"}}
	live, err := env.NewLive[C](env.WithSource(src), env.WithPollInterval(20*time.Millisecond),
		env.WithReloadDebounce(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	src.set("V", "b")
	select {
	case ev := <-live.Events():
		if ev.Err != nil || live.Get().V != "b" {
			t.Fatalf("event %+v, V=%q", ev, live.Get().V)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no reload; V=%q", live.Get().V)
	}
}

func TestLive_QuietPollEmitsNoEvent(t *testing.T) {
	t.Parallel()
	type C struct {
		V string `env:"V"`
	}
	live, err := env.NewLive[C](env.WithSource(&lockedMap{m: map[string]string{"V": "a"}}),
		env.WithPollInterval(10*time.Millisecond), env.WithReloadDebounce(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	select {
	case ev := <-live.Events():
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestLive_SteadyChangesStillReload(t *testing.T) {
	t.Parallel()
	src := newPushSource(map[string]string{"FOO": "0"})
	live, err := env.NewLive[liveCfg](env.WithSource(src), env.WithReloadDebounce(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				src.set("FOO", strconv.Itoa(i))
			}
		}
	}()
	defer func() { close(stop); <-done }()
	select {
	case ev := <-live.Events():
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a steady stream of changes postponed the reload indefinitely")
	}
}

type failingWatcher struct{}

func (failingWatcher) Lookup(string) (string, bool) { return "", false }

func (failingWatcher) Watch(context.Context) (<-chan env.Change, error) {
	return nil, errors.New("subscribe refused")
}

func TestLive_SubscribeFailureReported(t *testing.T) {
	t.Parallel()
	live, err := env.NewLive[liveCfg](env.WithSource(failingWatcher{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	select {
	case ev := <-live.Events():
		if ev.Err == nil || !strings.Contains(ev.Err.Error(), "subscribe refused") {
			t.Fatalf("event %+v, want subscribe error", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("subscribe failure not reported")
	}
}

func TestLive_PollRecoveryEmitsEvent(t *testing.T) {
	t.Parallel()
	type C struct {
		N int `env:"N"`
	}
	src := &lockedMap{m: map[string]string{"N": "1"}}
	live, err := env.NewLive[C](env.WithSource(src), env.WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	src.set("N", "bad")
	if ev := awaitEvent(t, live.Events(), "failed poll"); ev.Err == nil {
		t.Fatalf("event %+v, want Err", ev)
	}
	src.set("N", "1")
	for {
		ev := awaitEvent(t, live.Events(), "recovery")
		if ev.Err == nil {
			if len(ev.Changed) != 0 {
				t.Errorf("recovery Changed = %v, want none", ev.Changed)
			}
			return
		}
	}
}

func TestLive_ImmutableMutatorValueRedacted(t *testing.T) {
	t.Parallel()
	type C struct {
		Pass string `env:"PASS,immutable"`
	}
	resolve := env.MutatorFunc(func(_ context.Context, _, _, _, cur string, _ bool) (string, bool, error) {
		return strings.Replace(cur, "vault:", "resolved-", 1), false, nil
	})
	src := newPushSource(map[string]string{"PASS": "vault:one"})
	live, err := env.NewLive[C](env.WithSource(src), env.WithMutator(resolve), env.WithReloadDebounce(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	src.set("PASS", "vault:two")
	ev := awaitEvent(t, live.Events(), "immutable change")
	ice, ok := errors.AsType[*env.ImmutableChangedError](ev.Err)
	if !ok {
		t.Fatalf("ev.Err = %v, want *ImmutableChangedError", ev.Err)
	}
	if strings.Contains(ice.Old+ice.New, "resolved") {
		t.Errorf("Old/New = %q/%q, want redacted", ice.Old, ice.New)
	}
}
