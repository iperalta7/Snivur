package health

import (
	"encoding/json"
	"net/http"
	"snivur/v0/shared"
)

func HealthCheckHandler(writer http.ResponseWriter, request *http.Request) {
	writer.WriteHeader(http.StatusOK)
	json.NewEncoder(writer).Encode(shared.DefaultResponse{
		Status:  "200",
		Message: "Ok",
	})
}
