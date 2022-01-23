package main

import (
	"bytes"
	"fmt"

	"github.com/grafov/m3u8"
)

func ParseM3U8Urls(buf *bytes.Buffer) []string {
	playlist, listType, err := m3u8.Decode(*buf, false)

	if err != nil {
		fmt.Printf("Parse m3u8 url error: %s", err.Error())
		return nil
	}

	if listType != m3u8.MEDIA {
		fmt.Printf("Parse m3u8 url error: not a media playlist")
		return nil
	}

	mediaList := playlist.(*m3u8.MediaPlaylist)

	count := mediaList.Count()
	urls := make([]string, count)

	for i := uint(0); i < count; i++ {
		urls[i] = mediaList.Segments[i].URI
	}

	if key := mediaList.Key; key != nil {
		urls = append([]string{key.URI}, urls...)
	}

	return urls
}
