# Shadow Failure-Specific Evidence Progress

- [x] Inspect current shadow attempt, clone, and analyzer evidence paths.
- [x] Record the evidence scope and non-goals.
- [x] Add focused tests for ConfigMap key safety, runtime image/probes/readiness, metrics parsing, bounded samples/events, and concise redacted CLI output.
- [x] Implement typed evidence collection, optional memory sampling, source ConfigMap key resolution, and failure output.
- [x] Run focused and full Go tests; completed the fresh whole-branch review and fix pass.
- Ruling: memory metrics remain best-effort and do not gate verification — metrics.k8s.io may be absent or denied in real clusters, so treating it as required would turn telemetry coverage into a false repair verdict — cost if wrong: memory curves may be missing when an operator expects them.
- Ruling: source ConfigMap values are never emitted or retained; only object/key presence is stored — values can contain credentials despite ConfigMap semantics — cost if wrong: evidence may miss a value-level misconfiguration.
- Final: fixed probe-path query credential disclosure — `TestFailureEvidenceRedactsProbePathQueryAndPriorTermination` RED→GREEN; full `go test ./...` pass.
- Final: fixed optional metrics latency changing verification outcomes — `TestSlowOptionalMetricsDoNotDelayVerification` RED→GREEN; full `go test ./...` pass.
- Final: fixed CrashLoopBackOff masking last OOM termination — `TestFailureEvidenceRedactsProbePathQueryAndPriorTermination` RED→GREEN; full `go test ./...` pass.
- Final: fixed BinaryData keys reported as valid environment keys — `TestFailureEvidenceDoesNotTreatBinaryConfigMapKeyAsEnvironmentKey` RED→GREEN; full `go test ./...` pass.
- Final: fixed estimated Ready time presented as exact — `TestWriteShadowFailureRendersBoundedEvidenceWithoutProbeHost` RED→GREEN; full `go test ./...` pass.
- Final: fixed BinaryData key-type distinction for environment references while retaining volume-key support — `TestFailureEvidenceDoesNotTreatBinaryConfigMapKeyAsEnvironmentKey` and `TestFailureEvidenceAcceptsBinaryConfigMapKeyForVolume` RED→GREEN; full suite passes.
- Final: fixed metrics status when verification finishes before a sample — `TestSlowOptionalMetricsDoNotDelayVerification` RED→GREEN reports `not-observed`; full suite passes.
- Final: independent review found no Critical issues; all Important findings were fixed in one pass. The reviewer recorded no remaining blockers.
- Final: Ruling: existing `Attempt.Events` and `Attempt.Logs` serialization was set aside by review as pre-existing and outside this evidence slice — no changes made to avoid widening scope; cost if wrong: legacy fields may still expose content if their existing redaction path is insufficient.
- Final: Ruling: existing ConfigMap dependency gate behavior was set aside by review as pre-existing and outside this evidence slice — this work changes evidence reporting, not dependency eligibility; cost if wrong: a separate gate defect may still block or pass a candidate incorrectly.
