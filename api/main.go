package main

import (
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
	handler := newControllerServer(NewStore(time.Now), agents)
	log.Printf("Controller running on %s (agent %s)", cfg.Addr, cfg.AgentURL)
	log.Fatal(newHTTPServer(cfg.Addr, handler).ListenAndServe())
}
