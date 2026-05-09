// Package watcher provides a fsnotify-backed [env.Source] that
// re-parses a `.env` file when it changes on disk.
//
//	src, err := watcher.New(".env")
//	if err != nil { /* handle */ }
//	defer src.Close()
//
//	cfg, err := env.LoadTo[Config](env.WithSourceAppend(src))
//
// The returned [*Source] satisfies [env.Source] (so it composes into
// the loader's source chain) and [env.Watcher] (so [env.Loader.Watch]
// and [env.Live] can subscribe without polling).
//
// # Atomic-rename support
//
// The watcher observes the parent directory and filters events by
// filename. This makes editor "atomic save" patterns
// (write-temp-then-rename, delete-then-recreate, chmod) reliably
// observable, since the new inode the editor produces is a different
// file than the originally-watched one.
//
// # Debounce
//
// fsnotify event bursts are coalesced; the default quiet period is
// [DefaultDebounce] (75ms), configurable via [WithDebounce]. At most
// one [env.Change] is broadcast per quiet period.
//
// # Dependency direction
//
// This sub-package imports github.com/fsnotify/fsnotify; the root env
// and env/dotenv packages do not. Consumers who don't need file
// watching pay no transitive fsnotify cost.
package watcher
