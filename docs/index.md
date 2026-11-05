---
layout: default
title: Diagnose Kubernetes incidents with confidence
description: Fixora CLI connects Kubernetes evidence, optional AI explanations, and guarded remediation in one terminal workflow.
permalink: /
section: overview
---

<section class="hero"><div class="hero-inner"><div>
<span class="eyebrow">Kubernetes incident response, from your terminal</span>
<h1>Find the cause.<br>Review the fix.<br>Move with evidence.</h1>
<p>Fixora brings cluster signals, guided diagnosis, and optional AI into a clear workflow. Investigate first, inspect a concrete change, and choose when to verify or deliver it.</p>
<div class="actions"><a class="button" href="{{ '/get-started/' | relative_url }}">Get started</a><a class="button secondary" href="{{ '/use-cases/' | relative_url }}">Explore use cases</a></div>
<p class="hero-note">Open-source kubectl plugin · Runs locally · v0.8.0 documentation</p>
</div><div class="terminal" role="img" aria-label="Illustrative Fixora workflow: scan, explain, and preview a fix"><div class="terminal-bar">EXAMPLE WORKFLOW · ILLUSTRATIVE OUTPUT</div><pre><span class="prompt">$</span> kubectl fixora scan -n prod
<span class="warn">1 incident</span>  deployment/payments-api
<span class="dim">  Pods restarting · evidence available</span>

<span class="prompt">$</span> kubectl fixora why deployment/payments-api -n prod
<span class="good">Likely cause</span>  container image fails to start
<span class="dim">Proof: Pod state, Events, restart history</span>

<span class="prompt">$</span> kubectl fixora fix deployment/payments-api -n prod --preview
<span class="good">Plan ready for review</span>
<span class="dim">No cluster change made</span></pre></div></div></section>

<section class="section"><span class="eyebrow">A deliberate workflow</span><h2>From signal to a reviewed next step</h2><p class="section-intro">Use deterministic cluster evidence first. Add an AI explanation or a shadow check when the incident and your permissions call for it.</p><div class="steps"><div class="step"><span class="step-number">01 · DISCOVER</span><h3>Scan the cluster</h3><p>Find unhealthy workloads and collect bounded events, status, and logs by default. Use <code>--include-logs=false</code> to skip log reads.</p></div><div class="step"><span class="step-number">02 · UNDERSTAND</span><h3>Explain the evidence</h3><p>Inspect the likely cause, proof, confidence, and what still needs human judgment.</p></div><div class="step"><span class="step-number">03 · ACT WITH CONTROL</span><h3>Preview and verify</h3><p>Review a concrete patch before choosing local output, cluster apply, or source delivery.</p></div></div></section>

<section class="section tinted"><div class="section-inner"><span class="eyebrow">Start with the symptom</span><h2>Common investigations</h2><p class="section-intro">Every guide names the signals Fixora checks, a safe starting command, and the limits of the result.</p><div class="cards"><a class="card" href="{{ '/use-cases/workloads/' | relative_url }}"><h3>Pods keep restarting →</h3><p>Crash loops, probes, image pulls, OOM, and scheduling.</p></a><a class="card" href="{{ '/use-cases/networking/' | relative_url }}"><h3>Traffic cannot reach a service →</h3><p>Ingress, Gateway, Service selectors, endpoints, and DNS.</p></a><a class="card" href="{{ '/use-cases/storage-security/' | relative_url }}"><h3>A dependency blocks the rollout →</h3><p>PVCs, access policy, admission, and Secret references.</p></a></div><div class="callout"><strong>What verification means</strong><p>A shadow run tests an isolated clone against selected health signals. It does not prove that a production rollout will succeed. Fixora reports when verification was skipped or inconclusive. <a href="{{ '/safety/' | relative_url }}">Read the safety model →</a></p></div></div></section>

<section class="section"><span class="eyebrow">Operators stay in control</span><h2>Power when you need it. Clear boundaries always.</h2><div class="cards"><a class="card" href="{{ '/guides/ai/' | relative_url }}"><h3>Optional AI explanation →</h3><p>Use a configured provider with redacted evidence and review the answer against cluster facts.</p></a><a class="card" href="{{ '/guides/incident-workflow/' | relative_url }}"><h3>Guarded remediation →</h3><p>Inspect patch eligibility, dry-run results, shadow findings, and delivery options.</p></a><a class="card" href="{{ '/reference/' | relative_url }}"><h3>Complete command reference →</h3><p>Find commands, flags, analyzers, output formats, and configuration precedence.</p></a></div></section>
