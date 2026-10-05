package main

import (
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("controller: config error: %v", err)
	}

	handler := newControllerServer(cfg, newAgentClient())
	log.Printf("Controller running on %s (agent %s)", cfg.Addr, cfg.AgentURL)
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Mount("/", handler)
	log.Fatal(http.ListenAndServe(cfg.Addr, r))
}
