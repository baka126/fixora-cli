#!/usr/bin/env bash
set -euo pipefail
# Explicit local-only opt-in; never loads the developer's default kubeconfig.
cd "$(dirname "$0")/.."
for tool in docker kind go kubectl rg; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }; done
cluster="fixora-service-e2e"
registry="fixora-service-e2e-registry"
if kind get clusters | rg -qx "$cluster"; then echo "Refusing to reuse $cluster; delete it or choose a clean local test environment" >&2; exit 1; fi
if docker container inspect "$registry" >/dev/null 2>&1; then echo "Refusing to replace existing $registry" >&2; exit 1; fi
task_dir=$(mktemp -d)
created_cluster=false
created_registry=false
cleanup() {
  if $created_cluster; then kind delete cluster --name "$cluster"; fi
  if $created_registry; then docker rm -f "$registry"; fi
  rm -rf "$task_dir"
}
trap cleanup EXIT
cat > "$task_dir/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
YAML
kind create cluster --name "$cluster" --config "$task_dir/kind.yaml" --kubeconfig "$task_dir/kubeconfig"
created_cluster=true
docker run -d --name "$registry" --network kind -p 127.0.0.1:5001:5000 registry:2
created_registry=true
for node in $(kind get nodes --name "$cluster"); do
  docker exec "$node" mkdir -p /etc/containerd/certs.d/localhost:5001
  printf '[host."http://%s:5000"]\n  capabilities = ["pull", "resolve"]\n' "$registry" | docker exec -i "$node" sh -c 'cat > /etc/containerd/certs.d/localhost:5001/hosts.toml'
done
image="localhost:5001/fixora-service-probe:e2e"
arch=$(go env GOARCH)
docker build --build-arg TARGETARCH="$arch" -f build/service-probe.Dockerfile -t "$image" .
docker push "$image"
pinned=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image")
FIXORA_E2E_KIND_CLUSTER="$cluster" FIXORA_E2E_SERVICE_PROBE_IMAGE="$pinned" KUBECONFIG="$task_dir/kubeconfig" go test -tags='e2e,e2e_delivery' ./test/e2e -run '^TestServiceRoutingContractsAndRollback$' -count=1 -timeout=30m
