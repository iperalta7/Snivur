package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("controller: config error: %v", err)
	}

	agents := newHTTPAgentClient(cfg.AgentAPIKey, newAgentClient())
	registry := NewRegistry(time.Now, DefaultHealthThresholds())
	store := NewStore(time.Now)
	go runSweeper(context.Background(), registry, store, sweepInterval)
	handler := newControllerServer(store, registry, agents, cfg.AgentAPIKey, cfg.ControllerAPIKey)
	log.Printf("Controller running on %s", cfg.Addr)
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Mount("/", handler)
	log.Fatal(newHTTPServer(cfg.Addr, r).ListenAndServe())
}
