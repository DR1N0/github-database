# Changelog

All notable changes to this project are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.10] - 2026-10-29

### Changed

- **Breaking:** `TableDB.Table` and `GraphDB.Vertex` return `(T, error)` and
  report `ErrUnknownTable` / `ErrUnknownVertex` for names absent from
  `db_meta.json`. They previously returned a nil value, which panicked on the
  first method call. Callers must check the error once, at lookup time.
- An exhausted GitHub quota pauses the background flusher and poller until the
  reported reset time (at most 15 minutes) instead of retrying into a window
  that cannot succeed.

### Added

- `github.RateLimitError` carrying `Operation` and `ResetAt`; it still matches
  `errors.Is(err, github.ErrRateLimited)`.
- `ghdb.ErrUnknownTable` and `ghdb.ErrUnknownVertex` sentinels.
- Runnable examples for table mode, graph mode, and the unknown-table error.
- Fuzz targets for JSONL decoding and delta segmentation.
- `CHANGELOG.md`.

### Fixed

- Directory listings for branch names containing `/`: such a name cannot be
  passed to the trees endpoint as a ref (the slash reads as another path
  segment), so it is resolved through the branch head commit first.
  `/branches/...` requests now escape the name as well.

## [0.0.9] - 2026-10-29

### Fixed

- Graph checkpoints could not be reloaded. Vertex values now require an `id`
  string equal to the vertex id, replayed mutations are validated, and load
  reports a clear error for a missing id.
- `Get`, `All`, and `Set` no longer share byte slices with callers, so mutating a
  returned value or a passed buffer cannot corrupt in-memory state.
- A failed `Checkpoint` no longer disables later checkpoints on that instance; a
  snapshot branch left behind by the failed attempt is reused.
- Concurrent `flush` runs are serialized, removing a race between the background
  flusher and `Checkpoint`.
- `deriveInstanceID` returns an error instead of panicking.

### Changed

- HTTP requests escape paths and branch names, send a `User-Agent`, include
  GitHub's message in error responses, and report an exhausted quota as
  `ErrRateLimited`.
- Baseline-load warnings go through `Option.Logger`.
- `db_meta.json` must declare a mode and a repository path; online startup
  requires `base_repo`.
- `flush_interval_sec` defaults to 300 seconds. The README previously documented
  30.

## Earlier versions

No changelog was kept before 0.0.9.
