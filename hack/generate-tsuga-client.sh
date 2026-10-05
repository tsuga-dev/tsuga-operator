#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "") check=false ;;
  --check) check=true ;;
  *) echo "usage: $0 [--check]" >&2; exit 2 ;;
esac

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_dir="$(mktemp -d /tmp/tsuga-client-gen.XXXXXX)"
trap 'rm -rf "$work_dir"' EXIT

python3 "$repo_root/hack/tsuga-openapi-slice.py" \
  "$repo_root/public-open-api.json" "$work_dir/tsuga-api.json" --opaque

cat > "$work_dir/config.yaml" <<EOF
package: tsugaapi
output: $work_dir/tsuga.gen.go
generate:
  models: true
  client: true
output-options:
  response-type-suffix: HTTPResponse
EOF

(cd "$repo_root" && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 \
  -config "$work_dir/config.yaml" "$work_dir/tsuga-api.json")

if [[ "$check" == true ]]; then
  cmp "$work_dir/tsuga.gen.go" "$repo_root/internal/tsugaapi/tsuga.gen.go"
else
  mkdir -p "$repo_root/internal/tsugaapi"
  cp "$work_dir/tsuga.gen.go" "$repo_root/internal/tsugaapi/tsuga.gen.go"
fi
