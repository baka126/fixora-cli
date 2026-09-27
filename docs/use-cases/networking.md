---
layout: default
title: Networking failures
description: Trace Service, Ingress, Gateway, HTTPRoute, and DNS symptoms with Fixora.
permalink: /use-cases/networking/
section: use-cases
---

# Networking failures

Fixora connects Kubernetes routing resources and conditions. It cannot see every proxy, firewall, cloud load balancer, or application request path. Treat its trace as a map of visible cluster evidence.

## Service, Ingress, and Gateway routing

**Effect:** Reads Ingress or Gateway API route, Service, endpoints, and backing Pods. No cluster writes.

```bash
kubectl fixora trace ingress/api -n prod
kubectl fixora scan -n prod --filter Service,Ingress,Gateway,HTTPRoute
```

Check accepted route conditions, backend references, Service selectors, empty endpoints, target ports, and Pod readiness. Gateway API resources are optional; a missing CRD or denied read reduces that part of the trace. An accepted route does not guarantee external DNS, TLS, or cloud load balancer health.

## DNS failures

**Effect:** Reads Service DNS and CoreDNS-related cluster signals; no live application traffic is generated and no cluster object is written.

```bash
kubectl fixora dns -n prod
```

Compare the Service name, namespace, endpoints, and CoreDNS health signals. A lookup failure inside one Pod can also reflect its DNS policy, NetworkPolicy, or node path; use `trace` and the Pod's own diagnostics when the cluster-level view is inconclusive.

## Network policy context

**Effect:** Reads policy inventory when selected. No writes.

```bash
kubectl fixora scan -n prod --filter NetworkPolicy,Service,Pod
```

The NetworkPolicy analyzer supplies context; it does not simulate the full effective policy matrix for a specific packet. Review CNI behavior and other policy engines before changing access rules.
