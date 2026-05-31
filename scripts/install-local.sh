#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

go build -o bin/kubectl-fixora ./cmd/kubectl-fixora
install -m 0755 bin/kubectl-fixora "${1:-/usr/local/bin/kubectl-fixora}"
