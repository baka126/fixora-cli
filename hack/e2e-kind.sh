#!/usr/bin/env bash
set -euo pipefail

CLUSTER="${FIXORA_E2E_KIND_CLUSTER:-fixora-e2e}"
NS="${FIXORA_E2E_NAMESPACE:-fixora-e2e}"
BIN="${FIXORA_E2E_BIN:-./kubectl-fixora}"

if [[ "${FIXORA_E2E_CONFIRM:-}" != "yes" ]]; then
  cat <<EOF
This opt-in e2e scaffold creates a dedicated kind cluster/namespace and runs Fixora
against intentionally failing Kubernetes workloads.

Set FIXORA_E2E_CONFIRM=yes to run.

Scenarios covered or documented:
- CrashLoopBackOff pod
- ImagePullBackOff pod
- Deployment RCA resolving owned pods
- Service with no endpoints
- RBAC forbidden Secret read
- shadow retry with redaction enabled
- shadow retry disabled/fails closed when redaction disabled
- dry-run auto-fix
- malicious AI patch rejection
- cleanup behavior
EOF
  exit 2
fi

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

need kind
need kubectl

cleanup() {
  kubectl delete namespace "$NS" --ignore-not-found=true >/dev/null 2>&1 || true
  if [[ "${FIXORA_E2E_DELETE_CLUSTER:-yes}" == "yes" ]]; then
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER"
fi
kubectl config use-context "kind-$CLUSTER"
kubectl create namespace "$NS" --dry-run=client -o yaml | kubectl apply -f -

cat <<EOF | kubectl apply -n "$NS" -f -
apiVersion: v1
kind: Pod
metadata:
  name: crashloop
spec:
  containers:
  - name: app
    image: busybox:1.36
    command: ["sh", "-c", "echo password=hunter2; exit 1"]
---
apiVersion: v1
kind: Pod
metadata:
  name: imagepull
spec:
  containers:
  - name: app
    image: ghcr.io/fixora/does-not-exist:e2e
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: failing-deploy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: failing-deploy
  template:
    metadata:
      labels:
        app: failing-deploy
    spec:
      containers:
      - name: app
        image: ghcr.io/fixora/does-not-exist:e2e
---
apiVersion: v1
kind: Service
metadata:
  name: no-endpoints
spec:
  selector:
    app: missing
  ports:
  - port: 80
    targetPort: 8080
---
apiVersion: v1
kind: Secret
metadata:
  name: forbidden-secret
data:
  password: cGFzc3dvcmQ=
EOF

echo "Waiting briefly for failing states..."
sleep 10

if [[ ! -x "$BIN" ]]; then
  echo "Building $BIN"
  go build -o "$BIN" ./cmd/kubectl-fixora
fi

"$BIN" incidents -n "$NS" --include-logs --redact
"$BIN" analyze deployment/failing-deploy -n "$NS" --include-logs --redact
"$BIN" incidents -n "$NS" --filter service
"$BIN" patch deployment/failing-deploy -n "$NS" --container app --image busybox:1.36 --preview

echo "Manual follow-up checks:"
echo "- Shadow retry redaction enabled: run a concrete shadow verification with --shadow --shadow-retries 1 --redact."
echo "- Shadow retry fail-closed: repeat with --redact=false and confirm AI retry is blocked unless --unsafe-ai-no-redact is passed."
echo "- Malicious AI patch rejection: use a fake provider/mock test or inject metadata/hostPath/privileged patch and verify rejection."
echo "- Cleanup failure behavior: temporarily deny delete on pods/networkpolicies in $NS and verify cleanup warnings/errors."
