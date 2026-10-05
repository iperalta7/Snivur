package main

import (
	"log"
	"os"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("agent: config error: %v", err)
	}

	handler := newAgentServer(cfg, DockerRuntime{Run: ExecRunner})
	log.Printf("Agent listening on %s", cfg.Addr)
	log.Fatal(newHTTPServer(cfg.Addr, handler).ListenAndServe())
}
