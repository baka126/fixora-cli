#!/usr/bin/env bash
set -euo pipefail

if [[ "${FIXORA_E2E_HPA:-}" != "1" ]]; then
  echo "Set FIXORA_E2E_HPA=1 to opt in to creating a disposable local HPA test cluster" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
for tool in docker kind go kubectl; do
  command -v "$tool" >/dev/null || { echo "$tool is required for HPA acceptance" >&2; exit 1; }
done
docker info >/dev/null

task_dir=$(mktemp -d)
cluster="fixora-hpa-e2e-$(date +%s)-$$"
created=false
cleanup() {
  if $created; then kind delete cluster --name "$cluster"; fi
  rm -rf "$task_dir"
}
trap cleanup EXIT

if kind get clusters | grep -qx "$cluster"; then
  echo "Refusing to reuse an existing HPA acceptance cluster" >&2
  exit 1
fi

cat > "$task_dir/kind.yaml" <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
YAML

kind create cluster --name "$cluster" --config "$task_dir/kind.yaml" --kubeconfig "$task_dir/kubeconfig"
created=true
export KUBECONFIG="$task_dir/kubeconfig"
context="kind-$cluster"

kubectl --context "$context" apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml
kubectl --context "$context" -n kube-system patch deployment metrics-server --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
kubectl --context "$context" -n kube-system rollout status deployment/metrics-server --timeout=180s
kubectl --context "$context" wait --for=condition=Available apiservice/v1beta1.metrics.k8s.io --timeout=180s

FIXORA_E2E_HPA=1 FIXORA_E2E_KIND_CLUSTER="$cluster" KUBECONFIG="$task_dir/kubeconfig" \
  go test -tags='e2e,e2e_delivery' ./test/e2e -run '^TestHPARequestRepairOnDedicatedKind$' -count=1 -timeout=10m
