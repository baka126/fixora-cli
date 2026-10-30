# Shadow Failure-Specific Evidence Design

## Goal

Make a failed or inconclusive shadow run explain what was observed, using live candidate and source-template evidence rather than status-only guesses.

## Decisions

- Attach structured, bounded evidence for candidate container image reference/runtime `imageID`, termination reason and memory request/limit; Ready transition and time-to-ready; configured HTTP/TCP probe endpoint details and matching probe-failure events; ConfigMap reference/key resolution; and optional Pod memory metric samples.
- Query `metrics.k8s.io/v1beta1` through the existing dynamic client every 15 seconds during verification. Metrics are best effort: unsupported, denied, or missing metrics never change the verification verdict. Record only timestamp, container name, and memory quantity; do not collect CPU or unrelated metadata.
- Resolve ConfigMap existence and referenced-key presence in the source namespace without storing or emitting ConfigMap values. Do not read, copy, or disclose Secret data. Required ConfigMap dependencies remain omitted from the isolated namespace and produce an inconclusive result with key-resolution evidence.
- Keep the evidence model additive and bounded. Do not feed evidence back into patch generation or loosen delivery gates in this phase.
- Text output shows only relevant concise facts; JSON/YAML output carries structured evidence. Missing evidence is represented as unavailable/unknown, never as proof of a cause.

## Scope

Implement typed evidence collection, attach it to shadow results (including pre-candidate ConfigMap isolation blocks), render concise terminal evidence, test metrics/config/image/probe/time behavior, and document the permission and availability limits. No new API dependencies.
