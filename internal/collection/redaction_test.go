package collection

import (
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/tsuga-dev/tsuga-operator/internal/collection/assets"
)

func TestRedactionDefaultTokenKeys(t *testing.T) {
	var config struct {
		BlockedKeyPatterns []string `json:"blocked_key_patterns"`
	}
	if err := yaml.Unmarshal([]byte(assets.RedactionDefault), &config); err != nil {
		t.Fatal(err)
	}
	patterns := make([]*regexp.Regexp, 0, len(config.BlockedKeyPatterns))
	for _, p := range config.BlockedKeyPatterns {
		patterns = append(patterns, regexp.MustCompile(p))
	}
	blocked := func(key string) bool {
		for _, re := range patterns {
			if re.MatchString(key) {
				return true
			}
		}
		return false
	}
	for _, key := range []string{"token", "api_token", "x-token", "team.token", "auth_token", "X-Refresh-Token"} {
		if !blocked(key) {
			t.Errorf("%q is not blocked", key)
		}
	}
	for _, key := range []string{"gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "tokenizer"} {
		if blocked(key) {
			t.Errorf("%q is blocked", key)
		}
	}
}
