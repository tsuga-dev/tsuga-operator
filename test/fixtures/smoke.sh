#!/usr/bin/env bash
# Verifies a fixture image serves /work and logs to stdout. Run as:
#   test/fixtures/smoke.sh tsuga-e2e/app-nodejs:latest
set -euo pipefail

image="${1:?usage: smoke.sh <image>}"
container="$(docker run -d -P "$image")"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

port_mapping="$(docker port "$container" 8080/tcp)"
port="$(echo "$port_mapping" | head -1 | cut -d: -f2)"
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${port}/work" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

curl -fsS "http://127.0.0.1:${port}/work" >/dev/null \
  || { echo "FAIL: $image did not serve /work"; exit 1; }

sleep 3
logs="$(docker logs "$container" 2>&1)"
if [ -z "$logs" ]; then
  echo "FAIL: $image produced no stdout, the file_log receiver would see nothing"
  exit 1
fi
echo "OK: $image"
