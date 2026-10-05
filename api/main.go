package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("controller: config error: %v", err)
	}

	handler := newControllerServer(cfg, newAgentClient())
	log.Printf("Controller running on %s (agent %s)", cfg.Addr, cfg.AgentURL)
	log.Fatal(http.ListenAndServe(cfg.Addr, handler))
}
