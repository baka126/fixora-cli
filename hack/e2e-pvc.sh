#!/usr/bin/env bash
set -euo pipefail

# Destructive, opt-in test: provisions temporary EBS volumes and deletes only
# the unique namespace, PVs, and StorageClass created by this script. It never
# deletes a cluster.
if [[ "${FIXORA_PVC_E2E_CONFIRM:-}" != "yes" ]]; then
  cat <<'EOF'
This test provisions disposable EBS storage and applies policy-approved PVC
class assignment and expansion in a newly-created namespace. It creates a
temporary gp3 StorageClass with Delete reclaim policy, then cleans up its
namespace, PVs, and StorageClass. It never deletes a cluster or uses fixora-demo.

Set FIXORA_PVC_E2E_CONFIRM=yes to continue. The cluster must have the EBS CSI
driver installed. Optional: FIXORA_PVC_E2E_CONTEXT, FIXORA_PVC_E2E_BIN,
FIXORA_PVC_E2E_INITIAL_STORAGE (default 1Gi), FIXORA_PVC_E2E_TARGET_STORAGE
(default 2Gi), FIXORA_PVC_E2E_MAX_STORAGE (default 3Gi).
EOF
  exit 2
fi

BIN="${FIXORA_PVC_E2E_BIN:-./bin/kubectl-fixora}"
CTX="${FIXORA_PVC_E2E_CONTEXT:-}"
INITIAL="${FIXORA_PVC_E2E_INITIAL_STORAGE:-1Gi}"
TARGET="${FIXORA_PVC_E2E_TARGET_STORAGE:-2Gi}"
MAX="${FIXORA_PVC_E2E_MAX_STORAGE:-3Gi}"
need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }
need kubectl
need go
need python3
if [[ -z "$CTX" ]]; then
  CTX="$(kubectl config current-context)"
fi
if [[ -z "$CTX" ]]; then
  echo "a Kubernetes context is required" >&2
  exit 1
fi

if [[ ! -x "$BIN" ]]; then
  mkdir -p "$(dirname "$BIN")"
  go build -o "$BIN" ./cmd/kubectl-fixora
fi

K=(kubectl --context "$CTX")
F=("$BIN")

SUFFIX="$(date +%s)-$$-${RANDOM}"
RUN_ID="fixora-pvc-e2e-${SUFFIX}"
NS="fixora-pvc-e2e-${SUFFIX}"
SC_NAME="fixora-pvc-e2e-${SUFFIX}"
CLASS_PVC="class-check"
EXPAND_PVC="expand-check"
TMP="$(mktemp -d)"
DEFAULT_CLASSES=""
NS_CREATED=false
NS_UID=""
SC_CREATED=false
SC_UID=""
STOP_REQUESTED=0
restore_default_classes() {
  local pending=""
  while IFS= read -r default_sc; do
    [[ -n "$default_sc" ]] || continue
    if ! "${K[@]}" annotate storageclass "$default_sc" storageclass.kubernetes.io/is-default-class=true --overwrite >/dev/null 2>&1; then
      pending+="${pending:+$'\n'}${default_sc}"
      echo "warning: could not restore default StorageClass annotation: ${default_sc}" >&2
    fi
  done <<<"$DEFAULT_CLASSES"
  DEFAULT_CLASSES="$pending"
  [[ -z "$DEFAULT_CLASSES" ]]
}
cleanup() {
  local exit_status=$?
  trap - EXIT
  trap '' INT TERM
  local claim pv
  local pv_names=""
  local live_uid marker
  local cleanup_failed=false
  if [[ "$NS_CREATED" == "true" || -z "$NS_UID" ]]; then
    live_uid="$("${K[@]}" get namespace "$NS" -o 'jsonpath={.metadata.uid}' 2>/dev/null || true)"
    marker="$("${K[@]}" get namespace "$NS" -o 'jsonpath={.metadata.labels.fixora\.io/e2e-run}' 2>/dev/null || true)"
    if [[ -n "$live_uid" && "$marker" == "$RUN_ID" && ( -z "$NS_UID" || "$live_uid" == "$NS_UID" ) ]]; then
      for claim in "$CLASS_PVC" "$EXPAND_PVC"; do
        pv="$("${K[@]}" get pvc "$claim" -n "$NS" -o 'jsonpath={.spec.volumeName}' 2>/dev/null || true)"
        if [[ -n "$pv" ]]; then
          pv_names+="${pv_names:+$'\n'}${pv}"
        fi
      done
      if ! "${K[@]}" delete namespace "$NS" --ignore-not-found --wait=true --timeout=10m >/dev/null 2>&1; then
        echo "warning: namespace cleanup did not finish: $NS" >&2
        cleanup_failed=true
      fi
    else
      echo "warning: skipped namespace cleanup because its captured identity did not match: $NS" >&2
    fi
  fi
  while IFS= read -r pv; do
    [[ -n "$pv" ]] || continue
    if ! "${K[@]}" wait --for=delete "pv/${pv}" --timeout=5m >/dev/null 2>&1; then
      echo "warning: PV cleanup did not finish: ${pv}" >&2
      cleanup_failed=true
    fi
  done <<<"$pv_names"
  if [[ "$SC_CREATED" == "true" || -z "$SC_UID" ]]; then
    live_uid="$("${K[@]}" get storageclass "$SC_NAME" -o 'jsonpath={.metadata.uid}' 2>/dev/null || true)"
    marker="$("${K[@]}" get storageclass "$SC_NAME" -o 'jsonpath={.metadata.labels.fixora\.io/e2e-run}' 2>/dev/null || true)"
    if [[ -n "$live_uid" && "$marker" == "$RUN_ID" && ( -z "$SC_UID" || "$live_uid" == "$SC_UID" ) ]]; then
      if ! "${K[@]}" delete storageclass "$SC_NAME" --ignore-not-found >/dev/null 2>&1; then
        echo "warning: StorageClass cleanup failed: ${SC_NAME}" >&2
        cleanup_failed=true
      fi
    else
      echo "warning: skipped StorageClass cleanup because its captured identity did not match: $SC_NAME" >&2
    fi
  fi
  restore_default_classes || cleanup_failed=true
  if [[ "$cleanup_failed" == "true" || -n "$DEFAULT_CLASSES" ]]; then
    echo "error: one or more default StorageClass annotations could not be restored" >&2
    exit_status=1
  fi
  rm -rf "$TMP"
  exit "$exit_status"
}
stop_on_signal() {
  STOP_REQUESTED="$1"
  trap - INT TERM
  exit "$STOP_REQUESTED"
}
trap cleanup EXIT
trap 'stop_on_signal 130' INT
trap 'stop_on_signal 143' TERM

"${K[@]}" -n kube-system rollout status deployment/ebs-csi-controller --timeout=5m
"${K[@]}" -n kube-system rollout status daemonset/ebs-csi-node --timeout=5m
# Prevent admission from defaulting class-check before Fixora tests the allowed
# nil-to-class transition. Restore these exact original defaults on every exit.
DEFAULT_CLASSES="$("${K[@]}" get storageclass -o json | python3 -c 'import json,sys; doc=json.load(sys.stdin); print("\n".join(sc["metadata"]["name"] for sc in doc.get("items",[]) if sc.get("metadata",{}).get("annotations",{}).get("storageclass.kubernetes.io/is-default-class")=="true"))')"
while IFS= read -r default_sc; do
  [[ -n "$default_sc" ]] || continue
  "${K[@]}" annotate storageclass "$default_sc" storageclass.kubernetes.io/is-default-class=false --overwrite >/dev/null
done <<<"$DEFAULT_CLASSES"
SC_JSON="$(cat <<EOF | "${K[@]}" create -f - -o json
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${SC_NAME}
  labels:
    fixora.io/e2e-run: ${RUN_ID}
provisioner: ebs.csi.aws.com
parameters:
  type: gp3
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: Immediate
EOF
)"
SC_CREATED=true
SC_UID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["uid"])' <<<"$SC_JSON")"

NS_JSON="$("${K[@]}" create namespace "$NS" --dry-run=client -o json | python3 -c 'import json,sys; d=json.load(sys.stdin); d["metadata"].setdefault("labels", {})["fixora.io/e2e-run"] = sys.argv[1]; print(json.dumps(d))' "$RUN_ID" | "${K[@]}" create -f - -o json)"
NS_CREATED=true
NS_UID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["metadata"]["uid"])' <<<"$NS_JSON")"
cat <<EOF | "${K[@]}" apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${CLASS_PVC}
  namespace: ${NS}
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: ${INITIAL}
EOF

NO_POLICY_CODE=0
NO_POLICY_OUT="$("${F[@]}" fix --context "$CTX" "pvc/${CLASS_PVC}" -n "$NS" --no-ai --preview 2>&1)" || NO_POLICY_CODE=$?
printf '%s\n' "$NO_POLICY_OUT"
if [[ "$NO_POLICY_CODE" -ne 0 ]]; then
  echo "no-policy PVC preview exited ${NO_POLICY_CODE}" >&2
  exit 1
fi
grep -q -- '--pvc-policy' <<<"$NO_POLICY_OUT"
grep -q 'No changes made' <<<"$NO_POLICY_OUT"

expect_unsupported_action_refusal() {
  local action="$1"
  cat >"$TMP/refusal.yaml" <<EOF
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: refusal-e2e
  namespace: ${NS}
spec:
  pvc:
    name: ${CLASS_PVC}
  action: ${action}
EOF
  local result=0 output
  output="$("${F[@]}" fix --context "$CTX" "pvc/${CLASS_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/refusal.yaml" --preview 2>&1)" || result=$?
  if [[ "$result" -eq 0 ]] || ! grep -q 'No changes made' <<<"$output"; then
    printf 'unsafe action %s was not refused as expected:\n%s\n' "$action" "$output" >&2
    return 1
  fi
}

expect_unsupported_action_refusal delete
expect_unsupported_action_refusal setReclaimPolicy

cat >"$TMP/class.yaml" <<EOF
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: class-e2e
  namespace: ${NS}
spec:
  pvc:
    name: ${CLASS_PVC}
  action: setStorageClass
  storageClassName: ${SC_NAME}
EOF

"${F[@]}" fix --context "$CTX" "pvc/${CLASS_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/class.yaml" --preview
if [[ "$("${K[@]}" get pvc "$CLASS_PVC" -n "$NS" -o 'jsonpath={.spec.storageClassName}')" != "" ]]; then
  echo "preview unexpectedly assigned the StorageClass" >&2
  exit 1
fi
"${F[@]}" fix --context "$CTX" "pvc/${CLASS_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/class.yaml" --delivery=cluster --yes
if [[ "$("${K[@]}" get pvc "$CLASS_PVC" -n "$NS" -o 'jsonpath={.spec.storageClassName}')" != "$SC_NAME" ]]; then
  echo "applied class did not match approved StorageClass" >&2
  exit 1
fi
restore_default_classes
"${K[@]}" wait -n "$NS" --for=jsonpath='{.status.phase}'=Bound "pvc/${CLASS_PVC}" --timeout=10m

cat <<EOF | "${K[@]}" apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${EXPAND_PVC}
  namespace: ${NS}
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: ${SC_NAME}
  resources:
    requests:
      storage: ${INITIAL}
EOF
"${K[@]}" wait -n "$NS" --for=jsonpath='{.status.phase}'=Bound "pvc/${EXPAND_PVC}" --timeout=10m

cat >"$TMP/expand.yaml" <<EOF
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: expand-e2e
  namespace: ${NS}
spec:
  pvc:
    name: ${EXPAND_PVC}
  action: expand
  requestedStorage: ${TARGET}
  maxStorage: ${MAX}
EOF

"${F[@]}" fix --context "$CTX" "pvc/${EXPAND_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/expand.yaml" --preview
if [[ "$("${K[@]}" get pvc "$EXPAND_PVC" -n "$NS" -o 'jsonpath={.spec.resources.requests.storage}')" != "$INITIAL" ]]; then
  echo "preview unexpectedly changed requested capacity" >&2
  exit 1
fi
"${F[@]}" fix --context "$CTX" "pvc/${EXPAND_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/expand.yaml" --delivery=cluster --yes
if [[ "$("${K[@]}" get pvc "$EXPAND_PVC" -n "$NS" -o 'jsonpath={.spec.resources.requests.storage}')" != "$TARGET" ]]; then
  echo "applied capacity did not match approved request" >&2
  exit 1
fi
# CSI expands the block device first; a disposable mount pod is required for
# Kubernetes to finish filesystem resize and report the new PVC capacity.
cat <<EOF | "${K[@]}" apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: resize-check
  namespace: ${NS}
spec:
  restartPolicy: Never
  containers:
  - name: resize-check
    image: public.ecr.aws/docker/library/busybox:1.36
    command: ["sh", "-c", "sleep 300"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: ${EXPAND_PVC}
EOF
"${K[@]}" wait -n "$NS" --for=condition=Ready pod/resize-check --timeout=5m
"${K[@]}" wait -n "$NS" --for="jsonpath={.status.capacity.storage}=${TARGET}" "pvc/${EXPAND_PVC}" --timeout=10m

cat >"$TMP/shrink.yaml" <<EOF
apiVersion: fixora.io/v1alpha1
kind: PVCRepairPolicy
metadata:
  name: shrink-refusal-e2e
  namespace: ${NS}
spec:
  pvc:
    name: ${EXPAND_PVC}
  action: expand
  requestedStorage: ${INITIAL}
  maxStorage: ${MAX}
EOF
SHRINK_CODE=0
SHRINK_OUT="$("${F[@]}" fix --context "$CTX" "pvc/${EXPAND_PVC}" -n "$NS" --no-ai --pvc-policy "$TMP/shrink.yaml" --preview 2>&1)" || SHRINK_CODE=$?
if [[ "$SHRINK_CODE" -eq 0 ]] || ! grep -q 'No changes made' <<<"$SHRINK_OUT"; then
  printf 'shrink request was not refused as expected:\n%s\n' "$SHRINK_OUT" >&2
  exit 1
fi

echo "PVC class assignment and expansion requests completed in ${NS}; the trap now removes this disposable namespace."
