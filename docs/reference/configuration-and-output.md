---
layout: default
title: Configuration and output
description: Settings precedence, output formats, and command exit behavior.
permalink: /reference/configuration-and-output/
section: reference
---

# Configuration and output

Fixora loads local settings, environment values, and CLI flags. For a command, an explicit flag takes precedence over the equivalent environment setting, which takes precedence over the config file and built-in default. `FIXORA_CONFIG` selects a non-default config path.

## Inspect configuration safely

**Effect:** Reads local configuration; no cluster access or write.

```bash
kubectl fixora config path
kubectl fixora config view
kubectl fixora config validate
```

`config set|unset|reset` writes local settings; `config export` emits a shareable redacted view. Do not include a provider key in issue reports. Environment variables include `FIXORA_AI_PROVIDER`, `FIXORA_AI_API_KEY`, `FIXORA_AI_MODEL`, `FIXORA_AI_BASE_URL`, `FIXORA_AI_PROFILE`, and `FIXORA_SERVE_TOKEN`. `auth set` is a local credential workflow. The current branch expands context/profile controls; consult that branch's help before using them.

## Output formats

`-o, --output` supports `text`, `json`, `yaml`, `markdown`, `sarif`, `junit`, and `prometheus` for applicable commands. A specialist command may support a narrower set or a special format such as a Mermaid graph. Structured output is useful for automation, but keep evidence and skipped checks alongside a root-cause summary.

**Effect:** Reads cluster incidents and bounded logs by default, then prints JSON to stdout; use `--include-logs=false` to skip logs. No write.

```bash
kubectl fixora scan -n prod -o json
```

`--out` writes a local report or bundle where supported. That is different from a remediation patch or a production apply.

## Exit behavior

`0` means the command completed; it does not mean the cluster is healthy or a finding was verified. Flag/argument errors commonly exit `2`; operational errors commonly exit `1`. For automation, test the command and inspect structured fields such as findings, blocked reasons, verification status, and skipped checks. Do not infer remediation success from a zero exit alone.
