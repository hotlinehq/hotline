//go:build loadprofile

package main

// A build for the load test only (go build -tags loadprofile): when the
// load runner sets HOTLINE_LOAD_PPROF_ADDR, the proxy also serves Go's
// profiling pages there, so the runner can save where processor time and
// memory go. The program that ships is built without the tag and has none of
// this.

import (
	"net"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // load test build only, on 127.0.0.1
	"os"
	"time"
)

func init() {
	addr := os.Getenv("HOTLINE_LOAD_PPROF_ADDR")
	if addr == "" {
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return
	}
	go func() {
		srv := &http.Server{Handler: http.DefaultServeMux, ReadHeaderTimeout: 5 * time.Second}
		_ = srv.Serve(ln)
	}()
}
