#!/bin/sh

OUTPUT=${1:-"build/m3u8-parallel-downloader"}
echo "Building $OUTPUT"
CGO_ENABLED=0 go build --trimpath --ldflags '-s -w' -o $OUTPUT
