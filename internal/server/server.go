package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
)

type Options struct {
	Addr        string
	Kubectl     kube.Kubectl
	AnalyzerOpt analyzer.Options
	Token       string
}

func Serve(ctx context.Context, opts Options) error {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8089"
	}
	mux := http.NewServeMux()
	a := analyzer.New(opts.Kubectl, opts.AnalyzerOpt)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/analyzers", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, opts.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, analyzer.ListAnalyzers(nil))
	})
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, opts.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		findings, err := a.ScanIncidents(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, findings)
	})
	mux.HandleFunc("/analyze/", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, opts.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resource := strings.TrimPrefix(r.URL.Path, "/analyze/")
		finding, err := a.AnalyzeResource(r.Context(), resource)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, finding)
	})
	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+token
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
