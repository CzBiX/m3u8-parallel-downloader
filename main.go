package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

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

	if *chunkNum < 1 {
		fmt.Println("chunkNum must be greater than 0")
		os.Exit(1)
	}

	downloader := NewDownloader(*input, *chunkNum, *workerNum)

	server := &Server{
		Addr:       *addr,
		Downloader: downloader,
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Printf("\nReceived %v, shutting down...\n", sig)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("HTTP server shutdown error: %v\n", err)
		}
		downloader.Close()
	}()

	if err := server.Start(); err != nil && err != http.ErrServerClosed {
		fmt.Println(err)
		os.Exit(1)
	}
}
