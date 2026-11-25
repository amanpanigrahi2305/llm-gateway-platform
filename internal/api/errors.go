package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/providers"
	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/router"
)

// statusClientClosed is logged when the client disconnects before the answer
// is done. 499 is nginx's convention; it is only logged, never sent.
const statusClientClosed = 499

// apiError is an error in OpenAI's format: {"error":{"message","type","code"}}.
type apiError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e apiError) body() map[string]any {
	return map[string]any{"message": e.Message, "type": e.Type, "code": e.Code}
}

func badRequest(msg string) *apiError {
	return &apiError{http.StatusBadRequest, "invalid_request_error", "invalid_request", msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, e apiError) {
	writeJSON(w, e.Status, map[string]any{"error": e.body()})
}

// upstreamError turns a provider error into the error the client sees, plus a
// short error type for the log. A 4xx that the request itself caused is passed
// through; failures on the provider side become 502, or 504 for a timeout.
func upstreamError(err error) (apiError, string) {
	if errors.Is(err, router.ErrCircuitOpen) {
		return apiError{http.StatusServiceUnavailable, "api_error", "providers_unavailable",
			"every provider for this model is failing right now; try again later"}, "circuit_open"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return apiError{http.StatusGatewayTimeout, "api_error", "upstream_timeout", "upstream provider timed out"}, "upstream_timeout"
	}
	var pe *providers.Error
	if !errors.As(err, &pe) {
		return apiError{http.StatusInternalServerError, "api_error", "internal_error", "internal error"}, "internal"
	}
	switch pe.Status {
	case http.StatusTooManyRequests:
		return apiError{http.StatusTooManyRequests, "rate_limit_error", "upstream_rate_limited",
			"upstream provider " + pe.Provider + " is rate limiting requests"}, "upstream_429"
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return apiError{pe.Status, "invalid_request_error", "upstream_rejected_request", pe.Message}, "upstream_4xx"
	case http.StatusUnauthorized, http.StatusForbidden:
		// The gateway's own provider key is wrong. That is not the client's fault.
		return apiError{http.StatusBadGateway, "api_error", "upstream_auth_failed",
			"gateway could not authenticate to upstream provider " + pe.Provider}, "upstream_auth"
	}
	msg := "upstream provider " + pe.Provider + " failed"
	if pe.Status != 0 {
		msg += fmt.Sprintf(" (status %d)", pe.Status)
	}
	if pe.Message != "" {
		msg += ": " + truncate(pe.Message, 300)
	}
	return apiError{http.StatusBadGateway, "api_error", "upstream_error", msg}, "upstream_error"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
