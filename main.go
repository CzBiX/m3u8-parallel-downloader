package main

import (
	"flag"
	"fmt"
	"os"
)

const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/97.0.4692.71 Safari/537.36"

var addr = flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
var workerNum = flag.Int("worker", 3, "number of workers")
var chunkNum = flag.Int("chunk", 10, "number of buffered chunks")
var input = flag.String("input", "", "Input m3u8 url")

func main() {
	flag.Parse()

	if *input == "" {
		fmt.Println("input url is empty")
		os.Exit(1)
	}

	if *chunkNum < *workerNum {
		fmt.Println("chunkNum must be greater than or equal to workerNum")
		os.Exit(1)
	}

	downloader := NewDownloader(*input)

	server := &Server{
		Addr:       *addr,
		Downloader: downloader,
	}

	if err := server.Start(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
