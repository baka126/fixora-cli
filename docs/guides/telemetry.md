---
layout: default
title: Optional fix-session telemetry
description: Send a minimal, identifier-free fix outcome to an operator-configured HTTPS collector.
permalink: /guides/telemetry/
section: guides
---

# Optional fix-session telemetry

Telemetry is available on the development branch and is **off by default**. Fixora has no default collector. To send one report for a `fix` command, explicitly pass `--telemetry` and configure an HTTPS endpoint you trust.

```sh
export FIXORA_TELEMETRY_URL=https://telemetry.example.com/fixora/session
# Optional: bearer token for the configured endpoint.
export FIXORA_TELEMETRY_AUTH_TOKEN='your-collector-token'

kubectl fixora fix deployment/api -n prod --telemetry --preview
```

`--no-telemetry` disables reporting even if `--telemetry` is also present. Without `--telemetry`, Fixora sends nothing, even when the endpoint variables are set. The URL must use HTTPS and must not contain credentials, query parameters, or a fragment. Fixora does not follow redirects. The optional bearer token is sent only to the configured endpoint. Do not put a secret in the URL.

## Report fields

The version 1 JSON report contains only these fields:

| Field | Contents |
| --- | --- |
| `schemaVersion` | `1` |
| `clientVersion` | Fixora build version |
| `failureType` | Coarse category such as `oom`, `crash_loop`, `image_pull`, `probe`, `configuration`, `dependency_or_routing`, or `other` |
| `stage` | Furthest workflow stage: diagnosis, candidate, verification, or delivery |
| `verdict` | `not_run`, `verified`, `failed`, `inconclusive`, or `blocked` |
| `retries` | Number of candidate retries, excluding the initial attempt |
| `parity` | Optional score from 0 to 100 when shadow verification produced one |
| `applied` | Whether Fixora successfully sent a cluster apply request |

The report excludes target names, namespaces, image references, patches, logs, events, AI prompts and responses, and configuration or Secret values. It contains no Fixora-generated session ID or timestamp. As with any network request, the collector can observe normal connection metadata such as the source IP address; review its privacy and retention policy before opting in. Fixora sends one best-effort request at command completion; collector errors produce a warning and never change the fix result.

The operator configuring the collector is responsible for its access controls and retention period. Review the collector's policy before opting in. Fixora does not include server-side telemetry processing or aggregation.
