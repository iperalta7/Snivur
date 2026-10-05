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

	agents := newHTTPAgentClient(cfg.AgentURL, cfg.AgentAPIKey, newAgentClient())
	registry := NewRegistry(time.Now, DefaultHealthThresholds())
	go runSweeper(context.Background(), registry, sweepInterval)
	handler := newControllerServer(NewStore(time.Now), registry, agents, cfg.AgentAPIKey)
	log.Printf("Controller running on %s (agent %s)", cfg.Addr, cfg.AgentURL)
	log.Fatal(newHTTPServer(cfg.Addr, handler).ListenAndServe())
}
