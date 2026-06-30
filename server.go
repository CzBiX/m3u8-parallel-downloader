package main

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type Server struct {
	Addr       string
	Downloader *Downloader

	server *http.Server
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	path := request.URL.Path
	if path == "/" {
		path = INDEX_FILE_NAME
	} else {
		path = path[1:]
	}

	result, err := s.Downloader.Get(path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", result.ContentType)
	if _, err := result.Data.WriteTo(writer); err != nil {
		fmt.Printf("write response for %s: %v\n", path, err)
	}
	result.Close()
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:         s.Addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // streaming responses have no fixed deadline
		IdleTimeout:  60 * time.Second,
	}

	fmt.Printf("Starting server on %s\n", s.Addr)
	fmt.Printf("ffmpeg -i http://%s -c copy output.mkv\n", s.Addr)

	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}
