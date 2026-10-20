package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/pprof"
	"time"

	"helios/backend/internal/platform/config"
)

// startPprof serves Go's profiling endpoints on their own loopback-only listener when PPROF_ADDR is set. It is never
// on the public listener, and an address that is not loopback stops the process (see config.ResolvePprofAddr).
func startPprof(ctx context.Context, raw string) {
	addr, err := config.ResolvePprofAddr(raw)
	if err != nil {
		log.Fatal(err)
	}
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("pprof listening on %s (loopback only)", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pprof: %v", err)
		}
	}()
	go func() { <-ctx.Done(); _ = srv.Close() }()
}
