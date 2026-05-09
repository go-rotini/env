package watcher

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-rotini/env"
	"github.com/go-rotini/env/dotenv"
)

// --- helpers ---

// writeFile writes data to path with the standard test mode. Failing the
// test — rather than returning the error — makes call sites tighter.
func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// waitChange drains one event from ch with a generous timeout. The
// timeout is long enough that flakes only happen if the worker is
// genuinely stuck; the watcher's debounce is 75ms by default so a 3s
// upper bound provides ~40x headroom.
func waitChange(t *testing.T, ch <-chan env.Change, msg string) env.Change {
	t.Helper()
	select {
	case c, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed unexpectedly", msg)
		}
		return c
	case <-time.After(3 * time.Second):
		t.Fatalf("%s: timed out waiting for change event", msg)
	}
	return env.Change{}
}

// drainChange returns true if a Change arrives within d, false on
// timeout. Used for negative assertions ("no change should fire here").
func drainChange(ch <-chan env.Change, d time.Duration) (env.Change, bool) {
	select {
	case c := <-ch:
		return c, true
	case <-time.After(d):
		return env.Change{}, false
	}
}

// fastWatcher returns a watcher whose debounce is short enough that
// tests don't spend their lives waiting. Production callers get the
// 75ms default.
func fastWatcher(t *testing.T, path string, opts ...Option) *Source {
	t.Helper()
	all := append([]Option{WithDebounce(20 * time.Millisecond)}, opts...)
	s, err := New(path, all...)
	if err != nil {
		t.Fatalf("New(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// --- New / Lookup ---

func TestNew_InitialParse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\nBAZ=qux\n")

	s := fastWatcher(t, path)

	if v, ok := s.Lookup("FOO"); !ok || v != "bar" {
		t.Errorf("Lookup(FOO) = (%q, %v), want (bar, true)", v, ok)
	}
	if v, ok := s.Lookup("BAZ"); !ok || v != "qux" {
		t.Errorf("Lookup(BAZ) = (%q, %v), want (qux, true)", v, ok)
	}
	if _, ok := s.Lookup("MISSING"); ok {
		t.Error("Lookup(MISSING) returned ok=true")
	}
	if got := s.Path(); got != mustAbs(t, path) {
		t.Errorf("Path() = %q, want %q", got, path)
	}
}

func TestNew_MissingFileErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.env")

	if _, err := New(path); err == nil {
		t.Error("New on missing file should return an error")
	}
}

func TestNew_EmptyPathErrors(t *testing.T) {
	t.Parallel()
	_, err := New("")
	if !errors.Is(err, ErrEmptyPath) {
		t.Errorf("err = %v, want ErrEmptyPath", err)
	}
}

func TestNew_InvalidContentErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	// Unterminated single-quoted value triggers a dotenv parse error.
	writeFile(t, path, "FOO='unterminated\n")

	if _, err := New(path); err == nil {
		t.Error("New on syntactically invalid file should return an error")
	}
}

func TestNew_ForwardsParseOptions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FIRST=hello\nSECOND=$FIRST\n")

	// Without WithExpand, $FIRST stays literal.
	s1 := fastWatcher(t, path)
	if v, _ := s1.Lookup("SECOND"); v != "$FIRST" {
		t.Errorf("without expand: SECOND = %q, want literal", v)
	}

	// With expand forwarded through options.
	s2 := fastWatcher(t, path, WithParseOptions(dotenv.WithExpand()))
	if v, _ := s2.Lookup("SECOND"); v != "hello" {
		t.Errorf("with expand: SECOND = %q, want hello", v)
	}
}

// --- Watch / change events ---

func TestWatch_DirectWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=one\n")

	s := fastWatcher(t, path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeFile(t, path, "FOO=two\nNEW=added\n")
	change := waitChange(t, ch, "after direct write")

	if v, _ := s.Lookup("FOO"); v != "two" {
		t.Errorf("Lookup(FOO) = %q, want two", v)
	}
	if v, _ := s.Lookup("NEW"); v != "added" {
		t.Errorf("Lookup(NEW) = %q, want added", v)
	}
	if !slices.Contains(change.Keys, "FOO") || !slices.Contains(change.Keys, "NEW") {
		t.Errorf("change.Keys = %v, want both FOO and NEW", change.Keys)
	}
}

func TestWatch_AtomicRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=initial\n")

	s := fastWatcher(t, path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Editor "atomic save" pattern: write to a temp file, then rename
	// it over the target. fsnotify on the parent dir sees a Create or
	// Rename event for the target name; our worker filters by basename
	// and re-parses.
	tmp := filepath.Join(dir, ".env.tmp")
	writeFile(t, tmp, "FOO=replaced\n")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	change := waitChange(t, ch, "after atomic rename")
	if !slices.Contains(change.Keys, "FOO") {
		t.Errorf("change.Keys = %v, want to include FOO", change.Keys)
	}
	if v, _ := s.Lookup("FOO"); v != "replaced" {
		t.Errorf("Lookup(FOO) = %q, want replaced", v)
	}
}

func TestWatch_DeleteAndRecreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		// On Windows, fsnotify reliably reports the recreate but
		// deletion timing varies; skipping keeps CI deterministic.
		t.Skip("delete-and-recreate timing unreliable on Windows fsnotify")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=before\n")

	s := fastWatcher(t, path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Recreate. Even if a Change fires for the deletion (with the
	// re-parse failing and the old values retained), the recreation
	// must produce a Change with the NEW value.
	writeFile(t, path, "FOO=after\nFRESH=yes\n")

	// Drain up to two changes; the final state must reflect the new file.
	deadline := time.Now().Add(3 * time.Second)
	gotAfter := false
	for time.Now().Before(deadline) {
		select {
		case <-ch:
			if v, ok := s.Lookup("FOO"); ok && v == "after" {
				gotAfter = true
			}
			if gotAfter {
				if v, _ := s.Lookup("FRESH"); v != "yes" {
					t.Errorf("after recreate: FRESH = %q, want yes", v)
				}
				return
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatalf("did not observe FOO=after within deadline")
}

// --- debouncing ---

func TestWatch_DebounceCoalescesBurst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=0\n")

	// Use a relatively slow debounce so the burst clearly falls inside
	// one quiet period.
	s, err := New(path, WithDebounce(150*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// 5 writes in quick succession — well under the 150ms quiet period.
	for i := 1; i <= 5; i++ {
		writeFile(t, path, "FOO="+itoa(i)+"\n")
		time.Sleep(15 * time.Millisecond)
	}

	first := waitChange(t, ch, "first coalesced change")
	// Final value must be 5 (the last write); intermediate writes are
	// observed indirectly via the single coalesced event.
	if v, _ := s.Lookup("FOO"); v != "5" {
		t.Errorf("after burst: FOO = %q, want 5", v)
	}
	if !slices.Contains(first.Keys, "FOO") {
		t.Errorf("first.Keys = %v, want to include FOO", first.Keys)
	}

	// No second change should fire — the burst was a single quiet
	// period. Allow generous slack to weed out double-fire bugs.
	if _, more := drainChange(ch, 250*time.Millisecond); more {
		t.Error("got a second change event; debounce did not coalesce the burst")
	}
}

// --- multi-subscriber fan-out ---

func TestWatch_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=one\n")

	s := fastWatcher(t, path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch1, _ := s.Watch(ctx)
	ch2, _ := s.Watch(ctx)
	ch3, _ := s.Watch(ctx)

	writeFile(t, path, "FOO=two\n")

	var wg sync.WaitGroup
	for _, ch := range []<-chan env.Change{ch1, ch2, ch3} {
		wg.Go(func() {
			waitChange(t, ch, "fan-out subscriber")
		})
	}
	wg.Wait()
}

func TestWatch_ContextCancelClosesOnlyOneSubscriber(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=one\n")

	s := fastWatcher(t, path)

	ctx1, cancel1 := context.WithCancel(t.Context())
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()

	ch1, _ := s.Watch(ctx1)
	ch2, _ := s.Watch(ctx2)

	cancel1()

	// ch1 must close; ch2 must still receive.
	select {
	case _, ok := <-ch1:
		if ok {
			// Some impls deliver a final value before closing; drain
			// once more to confirm the close.
			if _, stillOpen := <-ch1; stillOpen {
				t.Error("ch1 should be closed after its context cancel")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ch1 did not close after context cancel")
	}

	writeFile(t, path, "FOO=two\n")
	waitChange(t, ch2, "ch2 after ch1 cancel")
}

// --- Close ---

func TestClose_IsIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\n")

	s, err := New(path, WithDebounce(20*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close must not panic and must not return a new error.
	if err := s.Close(); err != nil {
		t.Errorf("second Close returned %v, want nil", err)
	}
}

func TestClose_ClosesActiveSubscribers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\n")

	s, err := New(path, WithDebounce(20*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := s.Watch(ctx)

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			// Drain any in-flight change, then expect close.
			if _, stillOpen := <-ch; stillOpen {
				t.Error("subscriber channel still open after Close")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber channel did not close after Source.Close")
	}
}

func TestWatch_AfterCloseErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\n")

	s, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Watch(t.Context()); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}

func TestWatch_NilContextErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\n")

	s := fastWatcher(t, path)
	var nilCtx context.Context // intentional: exercises the nil-ctx guard
	if _, err := s.Watch(nilCtx); err == nil {
		t.Error("Watch(nil) should return an error")
	}
	if _, err := s.Watch(nilCtx); !errors.Is(err, ErrNilContext) {
		t.Errorf("err = %v, want ErrNilContext", err)
	}
}

// --- reload behavior on parse failure ---

func TestReload_KeepsValuesOnParseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=good\n")

	// Capture the worker's logger output to confirm the failure is
	// observable.
	var sb strings.Builder
	mu := &sync.Mutex{}
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &sb, mu: mu},
		&slog.HandlerOptions{Level: slog.LevelDebug}))

	s, err := New(path,
		WithDebounce(20*time.Millisecond),
		WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, _ := s.Watch(ctx)

	// Write a syntactically invalid file. The reload must fail; FOO
	// must retain its old value.
	writeFile(t, path, "FOO='unterminated\n")

	// Allow some time for the failed reload to be observed and logged.
	if _, more := drainChange(ch, 200*time.Millisecond); more {
		t.Error("a parse failure should NOT broadcast a change event")
	}
	if v, _ := s.Lookup("FOO"); v != "good" {
		t.Errorf("after parse failure: FOO = %q, want good (old value)", v)
	}
	mu.Lock()
	logged := sb.String()
	mu.Unlock()
	if !strings.Contains(logged, "reload failed") {
		t.Errorf("expected logger to capture reload failure, got %q", logged)
	}

	// Recover by writing a valid file — the watcher should start
	// emitting events again.
	writeFile(t, path, "FOO=better\n")
	waitChange(t, ch, "after recovery")
	if v, _ := s.Lookup("FOO"); v != "better" {
		t.Errorf("after recovery: FOO = %q, want better", v)
	}
}

// --- subscriber backpressure ---

func TestBroadcast_SlowSubscriberDoesNotBlockWorker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=0\n")

	s := fastWatcher(t, path)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow, _ := s.Watch(ctx)
	fast, _ := s.Watch(ctx)

	// Generate three sequential changes WITHOUT draining `slow`.
	// The slow subscriber's buffered chan (cap=1) accepts one Change;
	// subsequent Changes drop on the non-blocking send. The fast
	// subscriber must still receive every event the worker emits.
	writeFile(t, path, "FOO=1\n")
	waitChange(t, fast, "fast #1")
	writeFile(t, path, "FOO=2\n")
	waitChange(t, fast, "fast #2")
	writeFile(t, path, "FOO=3\n")
	waitChange(t, fast, "fast #3")

	// Now drain the slow channel — it must have at least one queued
	// event but never blocks the worker.
	select {
	case <-slow:
	case <-time.After(time.Second):
		t.Error("slow subscriber should have at least one queued event")
	}
}

// --- concurrent Lookup / reload (race detector) ---

func TestLookup_ConcurrentWithReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=0\n")

	s := fastWatcher(t, path)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = s.Lookup("FOO")
				}
			}
		})
	}

	for i := range 8 {
		writeFile(t, path, "FOO="+itoa(i)+"\n")
		time.Sleep(30 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

// --- diffKeys ---

func TestDiffKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		old  map[string]string
		next map[string]string
		want []string
	}{
		{
			name: "both empty",
			old:  map[string]string{},
			next: map[string]string{},
			want: nil,
		},
		{
			name: "added",
			old:  map[string]string{"A": "1"},
			next: map[string]string{"A": "1", "B": "2"},
			want: []string{"B"},
		},
		{
			name: "removed",
			old:  map[string]string{"A": "1", "B": "2"},
			next: map[string]string{"A": "1"},
			want: []string{"B"},
		},
		{
			name: "modified",
			old:  map[string]string{"A": "1"},
			next: map[string]string{"A": "2"},
			want: []string{"A"},
		},
		{
			name: "no change returns empty slice",
			old:  map[string]string{"A": "1"},
			next: map[string]string{"A": "1"},
			want: []string{},
		},
		{
			name: "mixed: result is sorted",
			old:  map[string]string{"B": "1", "C": "1"},
			next: map[string]string{"A": "1", "B": "2"},
			want: []string{"A", "B", "C"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := diffKeys(c.old, c.next)
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// --- matchesTarget (filename filtering) ---

func TestMatchesTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\n")

	s := fastWatcher(t, path)

	cases := []struct {
		in   string
		want bool
	}{
		{filepath.Join(dir, ".env"), true},            // exact match
		{filepath.Join(dir, ".env.tmp"), false},       // sibling: different basename
		{filepath.Join(dir, "subdir", ".env"), false}, // wrong directory
		{"", false}, // empty
	}
	for _, c := range cases {
		if got := s.matchesTarget(c.in); got != c.want {
			t.Errorf("matchesTarget(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// --- options helpers ---

func TestWithDebounce_ClampsNegative(t *testing.T) {
	t.Parallel()
	o := defaultOptions()
	WithDebounce(-1)(&o)
	if o.debounce != 0 {
		t.Errorf("WithDebounce(-1): debounce = %v, want 0", o.debounce)
	}
}

func TestWithLogger_NilIgnored(t *testing.T) {
	t.Parallel()
	o := defaultOptions()
	before := o.logger
	WithLogger(nil)(&o)
	if o.logger != before {
		t.Error("WithLogger(nil) should preserve the existing logger")
	}
}

func TestWithParseOptions_AccumulatesAcrossCalls(t *testing.T) {
	t.Parallel()
	o := defaultOptions()
	WithParseOptions(dotenv.WithExpand())(&o)
	WithParseOptions(dotenv.WithBackticks())(&o)
	if len(o.parseOptions) != 2 {
		t.Errorf("parseOptions len = %d, want 2", len(o.parseOptions))
	}
}

// --- debouncer unit tests ---

func TestDebouncer_FiresAfterQuietPeriod(t *testing.T) {
	t.Parallel()
	d := newDebouncer(50 * time.Millisecond)
	defer d.stop()

	d.tap()
	select {
	case <-d.c():
	case <-time.After(time.Second):
		t.Fatal("debouncer did not fire")
	}
}

func TestDebouncer_TapResets(t *testing.T) {
	t.Parallel()
	d := newDebouncer(80 * time.Millisecond)
	defer d.stop()

	d.tap()
	time.Sleep(40 * time.Millisecond)
	d.tap() // should reset; first 40ms doesn't count toward the new 80ms.
	time.Sleep(40 * time.Millisecond)
	// 40ms after the second tap; should NOT have fired yet.
	select {
	case <-d.c():
		t.Error("debouncer fired before quiet period elapsed")
	default:
	}
	// Wait the rest of the quiet period plus some slack.
	select {
	case <-d.c():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("debouncer did not fire after second quiet period")
	}
}

func TestDebouncer_ZeroDelayFiresImmediately(t *testing.T) {
	t.Parallel()
	d := newDebouncer(0)
	defer d.stop()

	d.tap()
	select {
	case <-d.c():
	case <-time.After(50 * time.Millisecond):
		t.Fatal("zero-delay debouncer did not fire immediately")
	}
}

// --- end-to-end through env.Loader ---

func TestEnvLoader_ConsumesWatcherSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "WATCHER_TEST_KEY=value-a\n")

	src := fastWatcher(t, path)
	loader := env.New(env.WithSource(src))

	type cfg struct {
		Key string `env:"WATCHER_TEST_KEY"`
	}
	var got cfg
	if err := loader.Load(&got); err != nil {
		t.Fatalf("loader.Load: %v", err)
	}
	if got.Key != "value-a" {
		t.Errorf("got.Key = %q, want value-a", got.Key)
	}
}

// --- snapshot helper used for diagnostics ---

func TestSnapshot_ReturnsCopy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, "FOO=bar\nBAZ=qux\n")

	s := fastWatcher(t, path)
	snap := s.snapshot()
	if snap["FOO"] != "bar" || snap["BAZ"] != "qux" {
		t.Errorf("snapshot = %v", snap)
	}
	// Mutating the snapshot must not affect the source.
	snap["FOO"] = "modified"
	if v, _ := s.Lookup("FOO"); v != "bar" {
		t.Errorf("Lookup(FOO) after snapshot mutation = %q, want bar", v)
	}
}

// --- compile-time interface assertions visible to readers ---

func TestCompileTime_InterfaceAssertions(t *testing.T) {
	t.Parallel()
	var (
		_ env.Source  = (*Source)(nil)
		_ env.Watcher = (*Source)(nil)
	)
	// Defensive sanity check via errors.Is on a fabricated error.
	if errors.Is(nil, errors.New("synthetic")) {
		t.Fatal("errors.Is(nil, ...) should be false")
	}
}

// --- small string helpers (stdlib-light) ---

// itoa returns the base-10 ASCII form of n. Used to sidestep importing
// strconv in tests just for itoa-equivalents.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", p, err)
	}
	return abs
}

// --- locked writer for concurrent slog handler ---

type lockedWriter struct {
	w  *strings.Builder
	mu *sync.Mutex
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}
