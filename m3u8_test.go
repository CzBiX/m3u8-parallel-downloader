package main_test

import (
	"bytes"
	"os"
	"testing"

	main "github.com/czbix/m3u8-parallel-downloader"

	"github.com/google/go-cmp/cmp"
)

func TestParseM3U8Urls(t *testing.T) {
	data, err := os.ReadFile("testdata/media.m3u8")
	if err != nil {
		panic(err)
	}

	urls := main.ParseM3U8Urls(bytes.NewBuffer(data))
	if !cmp.Equal(urls, []string{"key.ts", "1.ts", "2.ts", "3.ts"}) {
		t.Fail()
	}
}
