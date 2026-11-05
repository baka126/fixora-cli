# NetworkPolicy repair validation

This record covers the initial exact-spec NetworkPolicy replacement workflow. It is intentionally limited to
the behavior the implementation verifies; it does not certify general application health or all possible
Kubernetes traffic.

## Automated checks

The implementation was checked with:

```sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
make build
go test -c -tags='e2e,e2e_delivery' -o /tmp/fixora-networkpolicy-e2e.test ./test/e2e
python3 -m unittest tests.test_check_docs
bash -n hack/e2e-networkpolicy.sh
git diff --check
```

The tagged test compiles without contacting a cluster. Unit tests cover strict authority parsing, complete
inventory and refusal conditions, isolated probe semantics, receipts, spec-only apply, and rollback
response recovery.

## Real-CNI acceptance

`hack/e2e-networkpolicy.sh` is the opt-in acceptance harness. It provisions a uniquely named local Kind
cluster and private registry, installs Calico, builds a digest-pinned probe, then runs the tagged acceptance
test for allow/deny controls, spec replacement, observation, and restoration. It deletes only resources it
created.

The live harness was **not run in this environment** because Docker is unavailable. Therefore, the code has
automated unit/build validation, but live Calico behavior remains to be confirmed on a machine with Docker,
Kind, kubectl, Go, and registry image-pull access:

```sh
FIXORA_RUN_NETWORKPOLICY_KIND_E2E=1 ./hack/e2e-networkpolicy.sh
```

No shared or user EKS cluster was used or modified during this validation.
