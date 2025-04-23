package shared

type LaunchRequest struct {
	Game        string            `json:"game"`
	Config      map[string]string `json:"config"`
	Name        string            `json:"name"`
	AgentApiKey string            `json:"agent-apikey,omitempty"`
}

type LaunchResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}
