package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"go-proxy/internal/cache"
	"go-proxy/internal/config"
	"go-proxy/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	c := cache.New(cfg.CacheDir, cfg.CacheEnabled, cfg.MaxSizeBytes)
	h := proxy.New(cfg, c)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           logging(h.Routes()),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("go-proxy jalan di :%s -> %s (cache=%v dir=%s)", cfg.Port, cfg.TargetString, cfg.CacheEnabled, cfg.CacheDir)
	log.Printf("health: http://localhost:%s/__health", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen :%s: %v", cfg.Port, err)
		os.Exit(1)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// favicon biar tidak spam origin
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
