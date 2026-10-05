package tsugacli

import (
	"strings"
	"testing"
)

func TestConfigFromEnvReportsEveryMissingVariable(t *testing.T) {
	t.Setenv("TSUGA_E2E_API_TOKEN", "")
	t.Setenv("TSUGA_E2E_OTLP_ENDPOINT", "")
	t.Setenv("TSUGA_E2E_INGESTION_KEY", "")
	t.Setenv("TSUGA_E2E_CLUSTER_ID", "")

	_, err := ConfigFromEnv()
	if err == nil {
		t.Fatal("want an error when every credential is unset, got nil")
	}
	for _, name := range []string{
		"TSUGA_E2E_API_TOKEN", "TSUGA_E2E_OTLP_ENDPOINT",
		"TSUGA_E2E_INGESTION_KEY", "TSUGA_E2E_CLUSTER_ID",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should name the missing variable %q, got: %v", name, err)
		}
	}
}

// TestConfigFromEnvRejectsWhitespaceOnlyValues covers the shape a `== ""`
// check lets through: a variable holding a newline or a stray space, which a
// copy-paste or a `read` into an env file produces. It satisfies the guard
// and then fails 90 minutes later as an authentication error.
func TestConfigFromEnvRejectsWhitespaceOnlyValues(t *testing.T) {
	t.Setenv("TSUGA_E2E_API_TOKEN", "  \n\t ")
	t.Setenv("TSUGA_E2E_OTLP_ENDPOINT", "https://intake.example.tsuga.com")
	t.Setenv("TSUGA_E2E_INGESTION_KEY", "ing")
	t.Setenv("TSUGA_E2E_CLUSTER_ID", " ")

	_, err := ConfigFromEnv()
	if err == nil {
		t.Fatal("want an error when a required variable holds only whitespace, got nil")
	}
	for _, name := range []string{"TSUGA_E2E_API_TOKEN", "TSUGA_E2E_CLUSTER_ID"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should name the blank variable %q, got: %v", name, err)
		}
	}
	for _, name := range []string{"TSUGA_E2E_OTLP_ENDPOINT", "TSUGA_E2E_INGESTION_KEY"} {
		if strings.Contains(err.Error(), name) {
			t.Errorf("error should not name the variable %q, which is set: %v", name, err)
		}
	}
}

func TestConfigFromEnvDefaultsBaseURL(t *testing.T) {
	t.Setenv("TSUGA_E2E_API_TOKEN", "tok")
	t.Setenv("TSUGA_E2E_OTLP_ENDPOINT", "https://intake.example.tsuga.com")
	t.Setenv("TSUGA_E2E_INGESTION_KEY", "ing")
	t.Setenv("TSUGA_E2E_CLUSTER_ID", "abc-123")
	t.Setenv("TSUGA_E2E_BASE_URL", "")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://api.tsuga.com" {
		t.Fatalf("want default base URL, got %q", cfg.BaseURL)
	}
}
