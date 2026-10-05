package main

import (
	"context"
	"log"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Version is reported in heartbeats. Override with
// -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("agent: config error: %v", err)
	}

	if msg := advertiseWarning(os.Getenv); msg != "" {
		log.Printf("agent: WARNING: %s", msg)
	}

	rt := DockerRuntime{Run: ExecRunner}
	if cfg.ControllerURL == "" {
		log.Printf("agent: WARNING: SNIVUR_CONTROLLER_URL is not set; heartbeats are disabled")
	} else {
		hostname, err := os.Hostname()
		if err != nil {
			log.Printf("agent: hostname unavailable: %v", err)
		}
		hb := &Heartbeater{
			ControllerURL: cfg.ControllerURL,
			APIKey:        cfg.APIKey,
			Interval:      cfg.HeartbeatInterval,
			Runtime:       rt,
			Client:        newHeartbeatClient(),
			Identity: Identity{
				AgentID:  cfg.AgentID,
				Hostname: hostname,
				Address:  cfg.AdvertiseURL,
				Version:  Version,
			},
		}
		log.Printf("agent %s: heartbeating to %s every %s (advertising %s)", cfg.AgentID, cfg.ControllerURL, cfg.HeartbeatInterval, cfg.AdvertiseURL)
		go hb.Run(context.Background())
	}

	handler := newAgentServer(cfg, rt)
	log.Printf("Agent listening on %s", cfg.Addr)
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Mount("/", handler)
	log.Fatal(newHTTPServer(cfg.Addr, r).ListenAndServe())
}
