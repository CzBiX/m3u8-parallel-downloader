#!/bin/sh

OUTPUT=${1:-"build/m3u8-parallel-downloader"}

# append ext extension if not present
if [ "$GOOS" = "windows" -a "${OUTPUT: -4}" != ".exe" ]; then
    OUTPUT="${OUTPUT}.exe"
fi

echo "Building $OUTPUT"
CGO_ENABLED=0 go build --trimpath --ldflags '-s -w' -o $OUTPUT
