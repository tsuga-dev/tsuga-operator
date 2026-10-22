// Command app serves :8080 and calls itself every two seconds. It carries no
// OpenTelemetry code: spans in Tsuga prove the injected eBPF agent attached.
// /metrics serves the constant counter the kitchen sink's Prometheus scrapes
// look for.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

const addr = ":8080"

func logLine(fields map[string]any) {
	_ = json.NewEncoder(os.Stdout).Encode(fields)
}

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		logLine(map[string]any{"level": "info", "msg": "handled", "path": r.URL.Path})
		_, _ = w.Write([]byte("ok\n"))
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# TYPE e2e_fixture_requests_total counter\ne2e_fixture_requests_total 1\n"))
	})
	go func() {
		for range time.Tick(2 * time.Second) {
			resp, err := http.Get("http://127.0.0.1" + addr + "/work")
			if err != nil {
				logLine(map[string]any{"level": "warn", "msg": err.Error()})
				continue
			}
			_ = resp.Body.Close()
		}
	}()
	logLine(map[string]any{"level": "info", "msg": "listening", "port": 8080})
	log.Fatal(http.ListenAndServe(addr, nil))
}
