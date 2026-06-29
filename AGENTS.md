# AGENTS.md

Single-package Go module (`package main` at repo root, module `github.com/czbix/m3u8-parallel-downloader`). Not a monorepo. No Makefile, no lint config, no codegen.

## Build & run

```sh
go build .                          # local build
./scripts/build.sh                  # release build: CGO_ENABLED=0 --trimpath --ldflags '-s -w'
GOOS=windows ./scripts/build.sh     # cross-compile (auto-appends .exe)
```

Run, then consume the local proxy with ffmpeg in a second terminal:

```sh
./m3u8-parallel-downloader -input http://example.com/media.m3u8
ffmpeg -i http://127.0.0.1:8080 -c copy output.mkv
```

## Test

```sh
go test -v          # run from repo root; test reads testdata/media.m3u8 via relative path
```

## Architecture

This is **not** a download-to-disk CLI. It runs a local HTTP server that ffmpeg pulls from. Flow:

- `main.go` → `NewDownloader` (starts worker goroutines, immediately fetches the index m3u8 as idx 0) → `Server.Start` (`server.go`).
- `Downloader` (`downloader.go`) is the core: worker pool over a buffered `jobs` chan, LRU chunk cache (`cache` map + `order` slice), `sync.Cond` for get/prefetch coordination, `sync.Pool` for `bytes.Buffer` reuse. `maxRetries=5` with 3s backoff. Index 0 is the m3u8 itself; `ParseM3U8Urls` (`m3u8.go`) fills `urls`/`urlToIndex` under lock on first download.
- `Server.ServeHTTP` maps the request path to a url index and calls `Downloader.Get`, which blocks until that chunk is cached, then triggers `prefetch(idx+1)` for the next `capacity` segments.
- `Get` returns and closes the chunk (one-shot consumption); callers must `Close()`.
