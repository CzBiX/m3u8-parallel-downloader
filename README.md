# m3u8-parallel-downloader

A local HTTP proxy that parallel-downloads HLS (m3u8) segments and serves them to ffmpeg over a single local connection, using an in-memory chunk cache instead of downloading to disk.

## Usage

```sh
./m3u8-parallel-downloader -input http://example.com/media.m3u8
ffmpeg -i http://127.0.0.1:8080 -c copy output.mkv
```

### Flags

| Flag      | Default          | Description                          |
|-----------|------------------|--------------------------------------|
| `-input`  | *(required)*     | Input m3u8 url                       |
| `-addr`   | `127.0.0.1:8080` | HTTP listen address                  |
| `-worker` | `3`              | Number of parallel download workers  |
| `-chunk`  | `10`             | Number of buffered chunks (cache cap)|

## Build

```sh
go build .                          # local build
./scripts/build.sh                  # release build
GOOS=windows ./scripts/build.sh     # cross-compile (auto-appends .exe)
```

Requires Go 1.25+.
