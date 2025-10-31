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
	} else {
		path = path[1:]
	}

	result, err := s.Downloader.Get(path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", result.ContentType)
	result.Data.WriteTo(writer)
	result.Close()
}

func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:    s.Addr,
		Handler: s,
	}

	fmt.Println("Starting server on", s.Addr)

	return s.server.ListenAndServe()
}
