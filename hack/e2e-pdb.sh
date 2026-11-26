#!/usr/bin/env bash
set -euo pipefail
# A dedicated, uniquely named local kind cluster; never use the current context.
cd "$(dirname "$0")/.."
for tool in docker kind go kubectl; do
  command -v "$tool" >/dev/null || { echo "$tool is required; local PDB controller tests were not run" >&2; exit 1; }
done
task_dir=$(mktemp -d)
cluster="fixora-pdb-e2e-$(date +%s)-$$"
created=false
cleanup() {
  if $created; then kind delete cluster --name "$cluster"; fi
  rm -rf "$task_dir"
}
trap cleanup EXIT
existing_clusters=$(kind get clusters)
while IFS= read -r name; do
  if [[ "$name" == "$cluster" ]]; then
    echo "Refusing to reuse an existing PDB test cluster" >&2
    exit 1
  fi
done <<< "$existing_clusters"
cat > "$task_dir/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
YAML
kind create cluster --name "$cluster" --config "$task_dir/kind.yaml" --kubeconfig "$task_dir/kubeconfig"
created=true
FIXORA_E2E_PDB=1 FIXORA_E2E_KIND_CLUSTER="$cluster" KUBECONFIG="$task_dir/kubeconfig" \
  go test -tags='e2e,e2e_delivery' ./test/e2e -run '^TestPDBControllerContractsAndRollback$' -count=1 -timeout=20m
