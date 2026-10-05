package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestEmitSendsToConfiguredEndpoint(t *testing.T) {
	var mu sync.Mutex
	received := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stop := make(chan struct{})
	go func() {
		_ = emit(server.URL, "e2e-emitter", 50*time.Millisecond, stop)
	}()

	snapshot := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		return map[string]int{
			"/v1/metrics": received["/v1/metrics"],
			"/v1/traces":  received["/v1/traces"],
		}
	}

	deadline := time.After(10 * time.Second)
	for {
		counts := snapshot()
		if counts["/v1/metrics"] > 0 && counts["/v1/traces"] > 0 {
			break
		}
		select {
		case <-deadline:
			var missing []string
			if counts["/v1/metrics"] == 0 {
				missing = append(missing, "/v1/metrics")
			}
			if counts["/v1/traces"] == 0 {
				missing = append(missing, "/v1/traces")
			}
			t.Fatalf("emitter did not send to %v within 10s (received: %v)", missing, counts)
		case <-time.After(50 * time.Millisecond):
		}
	}
	close(stop)
}
