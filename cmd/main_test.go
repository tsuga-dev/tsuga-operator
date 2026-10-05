package main

import "testing"

func TestValidateBaseURL(t *testing.T) {
	for raw, wantErr := range map[string]bool{
		"https://api.tsuga.com":  false,
		"https://localhost:8443": false,
		"http://api.tsuga.com":   true,
		"api.tsuga.com":          true,
		"https://":               true,
		"HTTP://api.tsuga.com":   true,
	} {
		if err := validateBaseURL(raw); (err != nil) != wantErr {
			t.Errorf("validateBaseURL(%q) err=%v, wantErr=%v", raw, err, wantErr)
		}
	}
}
