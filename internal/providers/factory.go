package providers

import (
	"fmt"

	"github.com/amanpanigrahi2305/llm-gateway-platform/internal/config"
)

// New builds a provider from its config entry. getenv reads the provider's
// API key from the environment (os.Getenv in production, a fake in tests).
func New(name string, cfg config.Provider, getenv func(string) string) (Provider, error) {
	var apiKey string
	if cfg.APIKeyEnv != "" {
		apiKey = getenv(cfg.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("provider %s: environment variable %s is empty", name, cfg.APIKeyEnv)
		}
	}
	switch cfg.Type {
	case config.TypeOpenAI:
		return NewOpenAICompat(name, cfg.BaseURL, apiKey, cfg.Timeout, true), nil
	case config.TypeOllama, config.TypeMock:
		return NewOpenAICompat(name, cfg.BaseURL, apiKey, cfg.Timeout, false), nil
	case config.TypeAnthropic:
		return NewAnthropic(name, cfg.BaseURL, apiKey, cfg.Timeout), nil
	}
	return nil, fmt.Errorf("provider %s: unsupported type %q", name, cfg.Type)
}
