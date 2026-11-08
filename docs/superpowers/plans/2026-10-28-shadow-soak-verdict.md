# Shadow Soak Verdict Implementation Plan

1. Add tests for 60-second startup allowance, 120-second stability window, timeout inconclusive verdicts, and restart detection from the initial observation.
2. Implement startup/soak phases and retain successful batch completion behavior.
3. Map incomplete verification to `VerificationInconclusive`, suppress retries for inconclusive attempts, and separate analysis deadline from shadow verification in guided fix.
4. Update liveness/startup probe validation and user documentation for the three-minute observation horizon.
5. Run focused tests, then `go test ./...`; inspect the diff and record verification in the phase ledger.
