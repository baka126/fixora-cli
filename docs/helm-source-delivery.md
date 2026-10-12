# Validated Helm source delivery

Fixora can turn a reviewed image, resource, ConfigMap environment reference or probe patch into a values-file proposal. It uses the chart's actual render to check the effect rather than assuming chart key names. Arbitrary template changes and dedicated startup, placement and security policy source delivery remain manual review work.

Supply the local chart and the full render context used to deploy it:

```sh
kubectl fixora fix Deployment/api -n app --ai --preview \
  --repo /path/to/source \
  --helm-chart charts/api --helm-release api \
  --helm-render-mode upgrade \
  --helm-values environments/base.yaml \
  --helm-values environments/test.yaml \
  --helm-edit-values environments/test.yaml
```

Paths are relative to `--repo`. Values files are ordered; the editable file must be last. To edit chart defaults without overrides, explicitly select `<chart>/values.yaml` as the edit file. The chart and dependencies must already exist locally. Fixora does not download dependencies, extract release values, run post-renderers or run `helm upgrade`.

Fixora discovers Kubernetes version and API capabilities when they are omitted. To reproduce specific capabilities, set `--helm-kube-version` and repeat `--helm-api-version`, including kind-specific capabilities such as `apps/v1/Deployment` when the chart checks them. These inputs describe the operator's deployment model; unknown overrides cannot be inferred from live objects.

If multiple values paths produce the same repair, select a mapping explicitly:

```sh
--helm-map /spec/template/spec/containers/0/image=/component/reference
```

The pointers select a rendered field and values field. Mapping never bypasses render checks. The entire chart is rendered twice before and after each proposed edit; the target must exactly match the reviewed workload patch and all other rendered objects, including hooks and CRDs, must remain unchanged. Lint, chart schema checks and server dry-run admission checks are separate from runtime verification.

`--preview` and structured output never write source, create shadow workloads, commit or push. After reviewing a preview, use `--source-patch --yes` to write a source proposal, or `--delivery pr --yes` to write, commit, push and request review through the existing GitHub/GitLab tooling. `--commit --yes` stops after the local commit. Only the selected values file is committed; unrelated repository changes cause commit refusal.

Apply-eligible candidates require successful admission and matching shadow verification before source writing. Proposals blocked only by source ownership may be written after explicit review without a runtime receipt; confidence, recovery, ownership and controller blockers still prevent source writing. Their output says `runtime: not-requested`. Both outcomes say `reconciliation: pending`. A source change or PR does not prove production health. Source and workload identity are checked again before writing and before committing. Source delivery never applies the repair to production.

The writer uses a cooperative lock and atomic file replacement. It cannot make source edits, live Kubernetes reads and Git commits one transaction against unrelated concurrent writers. If source changes, prepare a fresh proposal rather than replaying saved JSON.

Failing or opaque unavailable cluster validation refuses CLI delivery. Charts with external schema references, nested packaged dependencies, nondeterministic template functions, unknown resource scopes or collateral effects require manual source review. Keep the existing read-only Helm suggestions when full render context is unavailable, and validate any manual change with `helm template` and the deployment workflow.
