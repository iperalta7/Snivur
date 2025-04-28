package shared

import (
	"encoding/json"
	"log"
	"net/http"
)

var (
	ApiKey = "default_api_key" // Replace with your actual API key
)

func ApiKeyMiddleware(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		log.Print("API Key: ", apiKey)
		if apiKey == "" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(DefaultResponse{
				Status:  "401",
				Message: "Unauthorized",
			})
			return
		}
		if apiKey != ApiKey {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(DefaultResponse{
				Status:  "403",
				Message: "Forbidden",
			})
			return
		}
		next.ServeHTTP(w, r)
	}
	return http.HandlerFunc(fn)
}
