#!/usr/bin/env bash
set -euo pipefail

if [[ "${FIXORA_RUN_NETWORKPOLICY_KIND_E2E:-}" != "1" ]]; then
  echo "Refusing to create a cluster. Set FIXORA_RUN_NETWORKPOLICY_KIND_E2E=1 to opt in." >&2
  exit 2
fi
for tool in docker kind kubectl go python3; do
  command -v "$tool" >/dev/null || { echo "Required command not found: $tool" >&2; exit 2; }
done
docker info >/dev/null

suffix="$(date +%s)-$$-${RANDOM}"
cluster="fixora-np-${suffix}"
registry="fixora-np-registry-${suffix}"
network="fixora-np-network-${suffix}"
port="${FIXORA_NP_REGISTRY_PORT:-5501}"
node_image='kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5'
work="$(mktemp -d "${TMPDIR:-/tmp}/fixora-np-e2e.XXXXXX")"
cluster_created=0
registry_created=0
network_created=0
umask 077

cleanup() {
  set +e
  if [[ "$cluster_created" == "1" ]]; then kind delete cluster --name "$cluster" --kubeconfig "$work/kubeconfig"; fi
  if [[ "$registry_created" == "1" ]]; then docker rm -f "$registry" >/dev/null; fi
  if [[ "$network_created" == "1" ]]; then docker network rm "$network" >/dev/null; fi
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

if kind get clusters 2>/dev/null | grep -Fxq "$cluster"; then
  echo "Refusing to reuse existing kind cluster $cluster" >&2
  exit 2
fi
if docker network inspect "$network" >/dev/null 2>&1 || docker container inspect "$registry" >/dev/null 2>&1; then
  echo "Refusing to reuse existing Docker resources" >&2
  exit 2
fi
if python3 - "$port" <<'PY'
import socket,sys
s=socket.socket();
try: s.bind(('127.0.0.1',int(sys.argv[1])))
except OSError: sys.exit(1)
finally: s.close()
PY
then :; else echo "Registry port $port is busy; select another with FIXORA_NP_REGISTRY_PORT" >&2; exit 2; fi

docker network create "$network" >/dev/null
network_created=1
docker run -d --name "$registry" --network "$network" -p "127.0.0.1:${port}:5000" registry:2 >/dev/null
registry_created=1
cat > "$work/kind.yaml" <<KIND
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
nodes:
  - role: control-plane
  - role: worker
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:${port}"]
      endpoint = ["http://${registry}:5000"]
KIND
cluster_created=1
KIND_EXPERIMENTAL_DOCKER_NETWORK="$network" kind create cluster --name "$cluster" --image "$node_image" --config "$work/kind.yaml" --kubeconfig "$work/kubeconfig" --wait 180s
export KUBECONFIG="$work/kubeconfig"
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.33.0/manifests/calico.yaml
kubectl rollout status daemonset/calico-node -n kube-system --timeout=240s
kubectl rollout status deployment/calico-kube-controllers -n kube-system --timeout=240s

probe_tag="localhost:${port}/fixora-service-probe:${suffix}"
docker build --platform linux/$(docker info --format '{{.Architecture}}' | sed 's/x86_64/amd64/; s/aarch64/arm64/') -t "$probe_tag" -f build/service-probe.Dockerfile .
docker push "$probe_tag"
probe_image="$(docker image inspect --format '{{index .RepoDigests 0}}' "$probe_tag")"
[[ "$probe_image" == *@sha256:* ]] || { echo "Could not resolve a pushed probe image digest" >&2; exit 1; }
export FIXORA_NETWORKPOLICY_PROBE_IMAGE="$probe_image"
kubectl apply -f test/e2e/fixtures/networkpolicy-repair/target.yaml
kubectl rollout status deployment/api -n fixora-np-e2e --timeout=180s
go test -count=1 -tags='e2e,e2e_delivery' -run '^TestNetworkPolicyRepairEnforcingCNI$' ./test/e2e
