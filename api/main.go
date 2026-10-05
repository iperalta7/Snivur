package main

import (
	"context"
	"log"
	"os"
	"time"
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
	handler := newControllerServer(store, registry, agents, cfg.AgentAPIKey)
	log.Printf("Controller running on %s", cfg.Addr)
	log.Fatal(newHTTPServer(cfg.Addr, handler).ListenAndServe())
}
