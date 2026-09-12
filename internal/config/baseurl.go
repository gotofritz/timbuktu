package config

// localDefaults are the base URLs the local providers fall back to when the
// config names none. They live here rather than in each provider factory
// because two places holding the same table is how `tbuk doctor` came to probe
// the empty string and report a reachable server as unreachable: the runtime
// resolved the default and the health check did not.
//
// A hosted provider is absent on purpose. Its adapter builds its own endpoint
// and there is nothing local to point at, so "" is the honest answer.
var localDefaults = map[string]string{
	"mlx":    "http://localhost:8080",
	"llama":  "http://localhost:8080",
	"ollama": "http://localhost:11434",
}

// DefaultBaseURL is where a provider is served when the config names no URL,
// or "" when it has no local default.
func DefaultBaseURL(provider string) string {
	return localDefaults[provider]
}

// ResolveBaseURL is the URL a provider will actually be reached at: the
// configured one, or its local default when that is empty.
func ResolveBaseURL(provider, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	return DefaultBaseURL(provider)
}
