#!/usr/bin/env bash
set -euo pipefail

workspace_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
audit_cache="${TMPDIR:-/tmp}/vani-integration-audit-go-cache"

for module in Context EnrichmentAPI lookup ProfileAPI; do
  echo "==> ${module}"
  (
    cd "${workspace_dir}/${module}"
    GOCACHE="${audit_cache}" go test -count=1 ./...
  )
done
