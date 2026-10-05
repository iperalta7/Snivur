package main

import (
	"log"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("agent: config error: %v", err)
	}

	handler := newAgentServer(cfg, DockerRuntime{Run: ExecRunner})
	log.Printf("Agent listening on %s", cfg.Addr)
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Mount("/", handler)
	log.Fatal(newHTTPServer(cfg.Addr, r).ListenAndServe())
}
