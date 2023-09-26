package main

import (
	"fmt"
	"net/http"
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
	}

	result := s.Downloader.GetResult(path)
	writer.Header().Set("Content-Type", result.ContentType)
	if path == INDEX_FILE_NAME {
		writer.Write(result.Data.Bytes())
	} else {
		result.Data.WriteTo(writer)
		result.Close()
	}
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:    s.Addr,
		Handler: s,
	}

	fmt.Println("Starting server on", s.Addr)

	return s.server.ListenAndServe()
}
