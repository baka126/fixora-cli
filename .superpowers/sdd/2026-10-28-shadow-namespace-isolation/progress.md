# SDD ledger — plan: docs/superpowers/plans/2026-10-28-shadow-namespace-isolation.md

Pre-flight: task interfaces are sequential; namespace primitives feed the run orchestrator, then isolation gate, candidate outcome classification, and cleanup. No interface conflict found in source.

Task 1: complete
Task 1: complete — `TestNamespaceOwnershipPrimitives` failed for missing methods, then passed after adding typed namespace create/get and UID-precondition delete. Commit: pending grouping with Task 2 because both are the run's namespace boundary.
Task 2: complete — run attempts now use a unique Fixora-owned namespace, deny-only validation, explicit verification statuses, and isolated candidate routing. `go test ./internal/shadow` passed. Commit: pending.
Ruling: Treat installed nonstandard NetworkPolicy APIs as blockers only when active policy objects are present; still fail closed on incomplete/forbidden object listing. — Calico/Cilium APIs may be installed on a cluster without any active cluster-wide rules, and blocking on API presence alone would make standard NetworkPolicy enforcement unverifiable in otherwise compatible clusters. — Cost if wrong: an unrecognized API that affects traffic but is not enumerated as a NetworkPolicy resource could weaken the assumption that only the sandbox NetworkPolicy governs the controls.
Task 3: complete — a digest-pinned fixed-purpose probe performs bidirectional positive/negative controls; active extension policy inventory and exact NetworkPolicy rechecks fail closed. Unit tests passed. Commit: pending.
Task 4: complete — required ConfigMaps and pull-secret dependencies are classified inconclusive before candidate creation; Secret, PVC, host, and external/shared storage cases remain blocked. Unit tests passed. Commit: pending.
Task 5: implementation complete — UID-guarded namespace cleanup, `--keep-shadow` restricted to proven isolation, CLI/docs, and isolated kind/Calico CI harness are in place. Full Go suite, build, E2E compilation, shell syntax, and diff checks passed. Live kind E2E remains unrun because Docker is unavailable in this environment. Commit: pending.
Review finding: `--keep-shadow` initially retained namespaces on failed isolation proof; corrected to always clean up unproven namespaces and added `TestKeepShadowDoesNotRetainNamespaceWhenIsolationProofFails`.
