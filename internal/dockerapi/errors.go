package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"wslc-bridge/internal/process"
)

var errUnsupportedAPIVersion = errors.New("Docker API versions older than 1.24 are not supported")

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func commandStatus(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	var commandError *process.CommandError
	if errors.As(err, &commandError) {
		message := strings.ToLower(commandError.Stderr)
		if strings.Contains(message, "not found") || strings.Contains(message, "no such") {
			return http.StatusNotFound
		}
		if strings.Contains(message, "already") || strings.Contains(message, "conflict") || strings.Contains(message, "running") {
			return http.StatusConflict
		}
		return http.StatusBadGateway
	}
	return http.StatusBadGateway
}
