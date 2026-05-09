// Package watcher provides a fsnotify-backed [env.Source] that re-parses
// a `.env` file when the file changes on disk.
//
// # Overview
//
//	src, err := watcher.New(".env")
//	if err != nil { /* handle */ }
//	defer src.Close()
//
//	cfg, err := env.Load[Config](env.WithSourceAppend(src))
//
// The returned [*Source] satisfies [env.Source] (so it composes with the
// rest of the loader's source chain) AND [env.Watcher] (so the loader's
// future Watch / Live[T] machinery can subscribe to file-change events
// without polling).
//
// # Atomic-rename support
//
// The watcher observes the **parent directory** of the target file rather
// than the file itself, then filters events by filename. This is the
// classic fsnotify trick that makes editor "atomic save" patterns
// (write-temp-then-rename, delete-then-recreate, chmod) all reliably
// observable, since the inode the editor produces is a different file
// than the one we initially saw.
//
// # Debounce
//
// Bursts of fsnotify events from a single save are coalesced. The default
// quiet period is 75ms, configurable via [WithDebounce]. The watcher
// re-parses and broadcasts at most one [env.Change] per quiet period.
//
// # Dependency direction
//
// This sub-package imports [github.com/fsnotify/fsnotify], so the fsnotify
// dependency is contained here. The root `env` package and the
// `env/dotenv` parser have zero third-party imports; consumers who don't
// need file watching pay no transitive cost.
package watcher
