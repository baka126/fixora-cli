---
layout: default
title: Use cases
description: Start with the Kubernetes symptom and find the right Fixora investigation.
permalink: /use-cases/
section: use-cases
---

# Use cases

Start with a **read-only** investigation. Fixora can connect resource status, Events, owner references, optional bounded logs, and selected analyzers, but a missing permission or unavailable CRD can leave gaps. Each guide gives an example command, the evidence to review, and the limit of the result.

| Symptom | Start here |
| --- | --- |
| Restarts, failed probes, image pulls, OOM, Pending Pods, or failed Jobs | [Workloads]({{ '/use-cases/workloads/' | relative_url }}) |
| Service has no endpoints, route is rejected, or DNS lookup fails | [Networking]({{ '/use-cases/networking/' | relative_url }}) |
| PVC is unbound, service account denied, or admission policy blocks a workload | [Storage and security]({{ '/use-cases/storage-security/' | relative_url }}) |
| Rollout is Helm/GitOps managed or a node is unhealthy | [Delivery and operations]({{ '/use-cases/delivery-operations/' | relative_url }}) |

For a broad first pass, [scan the namespace]({{ '/get-started/' | relative_url }}). Use [analyzer filters]({{ '/reference/analyzers/' | relative_url }}) only when you understand the coverage they remove.
