---
layout: default
title: Disposable demo acceptance scenario
description: Validate the guarded Fixora demo workflow against a disposable Kubernetes cluster.
permalink: /testing/disposable-demo/
section: testing
---

# Disposable demo acceptance scenario

Run only against a disposable Kubernetes cluster or a dedicated test context with enough spare CPU and memory to schedule one small Pod. The demo creates a run-identified temporary namespace and Deployment with deny-ingress/deny-egress policy, waits up to 60 seconds for actionable owned-Pod evidence, then sends an eligible concrete patch through Fixora's normal per-run shadow verifier. The demo never patches the fixture Deployment or performs patch, PR, or cluster delivery. An early `NoOwnedPods` or transient `Pending` result is not treated as the fixture diagnosis. If capacity is unavailable, it reports the last status and cleans up the namespace.

```sh
kubectl config current-context
kubectl fixora demo --preview
kubectl fixora demo --yes
kubectl fixora demo probe --yes --ai
```

Confirm the preview run reports no changes. The second run should show the fixture-specific diagnosis, shadow verdict, evidence module and operational parity when available, and `Cleaned up namespace/<generated-name>`. With `--ai` and a configured provider, Fixora may make at most two bounded candidate revisions after measured candidate failures; without a usable provider it reports that it will run one deterministic attempt. An inconclusive result is never retried. The verifier uses its own per-attempt shadow namespaces and removes failed attempts before continuing. Verify that the generated demo namespace and all shadow namespaces from the run no longer exist:

```sh
kubectl get namespace <generated-name>
```

Repeat with `config` and `oom`. The default `probe` fixture exposes one declared HTTP port and targets another, so the normal analyzer and inferrer can produce a concrete patch that the shadow verifier checks. A fixture with no apply-eligible concrete proposal stops before shadow creation and states why; Fixora must not invent a patch to make the demo appear successful. To validate interrupt cleanup, start a fixture and interrupt it while it waits for evidence; verify the namespace is removed. To validate partial setup cleanup, use a test identity allowed to create namespaces but denied Deployment creation; the command should fail and remove only its generated namespace. A namespace deletion denial should name the retained namespace and show the recovery command.

The command must not modify pre-existing namespaces or deliver/apply its suggested repair. Review API audit events or compare the target cluster before and after to confirm these properties.
