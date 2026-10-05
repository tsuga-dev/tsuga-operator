// Package tsugacli drives the tsuga CLI from the end-to-end suite.
package tsugacli

import (
	"fmt"
	"os"
	"strings"
)

const defaultBaseURL = "https://api.tsuga.com"

// Config holds the credentials the suite needs to reach the Tsuga test org.
type Config struct {
	APIToken     string
	OTLPEndpoint string
	IngestionKey string
	ClusterID    string
	BaseURL      string
}

// ConfigFromEnv reads the suite's credentials from the environment. The four
// required variables have no defaults: the suite refuses to run rather than
// guessing a target, which is what keeps it off demo and production.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		APIToken:     strings.TrimSpace(os.Getenv("TSUGA_E2E_API_TOKEN")),
		OTLPEndpoint: strings.TrimSpace(os.Getenv("TSUGA_E2E_OTLP_ENDPOINT")),
		IngestionKey: strings.TrimSpace(os.Getenv("TSUGA_E2E_INGESTION_KEY")),
		ClusterID:    strings.TrimSpace(os.Getenv("TSUGA_E2E_CLUSTER_ID")),
		BaseURL:      strings.TrimSpace(os.Getenv("TSUGA_E2E_BASE_URL")),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}

	// Whitespace counts as missing. A guard whose whole job is to fail
	// closed must not be satisfied by a variable holding a newline or a
	// stray space, which is what a copy-paste or a `read` into an env file
	// produces - and a blank token then fails 90 minutes later as an
	// authentication error rather than here as a missing credential.
	var missing []string
	for _, required := range []struct{ name, value string }{
		{"TSUGA_E2E_API_TOKEN", cfg.APIToken},
		{"TSUGA_E2E_OTLP_ENDPOINT", cfg.OTLPEndpoint},
		{"TSUGA_E2E_INGESTION_KEY", cfg.IngestionKey},
		{"TSUGA_E2E_CLUSTER_ID", cfg.ClusterID},
	} {
		if required.value == "" {
			missing = append(missing, required.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf(
			"missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}
