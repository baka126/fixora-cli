#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
cluster="${FIXORA_E2E_KIND_CLUSTER:-fixora-shadow-e2e}"
registry="${FIXORA_E2E_REGISTRY:-fixora-shadow-e2e-registry}"
probe_tag="localhost:5001/fixora-shadow-probe:e2e"
calico_version="v3.33.0"
kind_node_image="kindest/node:v1.34.0"
tmp_dir=$(mktemp -d)
created_cluster=false
created_registry=false

need() {
  command -v "$1" >/dev/null || { echo "$1 is required" >&2; exit 1; }
}
for tool in docker kind kubectl go rg; do need "$tool"; done

if kind get clusters | rg -qx "$cluster"; then
  echo "Refusing to reuse existing kind cluster $cluster" >&2
  exit 1
fi
if docker container inspect "$registry" >/dev/null 2>&1; then
  echo "Refusing to replace existing Docker container $registry" >&2
  exit 1
fi

cleanup() {
  if $created_cluster; then kind delete cluster --name "$cluster" >/dev/null 2>&1 || true; fi
  if $created_registry; then docker rm -f "$registry" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

cat > "$tmp_dir/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
YAML

kind create cluster --name "$cluster" --image "$kind_node_image" --config "$tmp_dir/kind.yaml" --kubeconfig "$tmp_dir/kubeconfig"
created_cluster=true
docker run -d --name "$registry" --network kind -p 127.0.0.1:5001:5000 registry:2 >/dev/null
created_registry=true

for node in $(kind get nodes --name "$cluster"); do
  docker exec "$node" mkdir -p /etc/containerd/certs.d/localhost:5001
  printf '[host."http://%s:5000"]\n  capabilities = ["pull", "resolve"]\n' "$registry" |
    docker exec -i "$node" sh -c 'cat > /etc/containerd/certs.d/localhost:5001/hosts.toml'
done

export KUBECONFIG="$tmp_dir/kubeconfig"
kubectl apply -f "https://raw.githubusercontent.com/projectcalico/calico/${calico_version}/manifests/calico.yaml"
kubectl wait --for=condition=Ready nodes --all --timeout=8m
kubectl -n kube-system rollout status daemonset/calico-node --timeout=8m

docker build --build-arg TARGETARCH="$(go env GOARCH)" -f build/service-probe.Dockerfile -t "$probe_tag" .
docker push "$probe_tag"
probe_image=$(docker image inspect --format '{{index .RepoDigests 0}}' "$probe_tag")
if [[ ! "$probe_image" =~ @sha256:[0-9a-f]{64}$ ]]; then
  echo "could not resolve a manifest digest for the shadow probe image" >&2
  exit 1
fi

tags="${FIXORA_E2E_TEST_TAGS:-e2e}"
timeout="${FIXORA_E2E_TIMEOUT:-25m}"
run_pattern="${FIXORA_E2E_RUN:-}"
if [[ -n "$run_pattern" ]]; then
  FIXORA_E2E_KIND_CLUSTER="$cluster" FIXORA_SHADOW_PROBE_IMAGE="$probe_image" \
    go test -tags "$tags" -timeout "$timeout" -v -run "$run_pattern" ./test/e2e/...
else
  FIXORA_E2E_KIND_CLUSTER="$cluster" FIXORA_SHADOW_PROBE_IMAGE="$probe_image" \
    go test -tags "$tags" -timeout "$timeout" -v ./test/e2e/...
fi
