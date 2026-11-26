package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/storagefix"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

type pvcRepairReport struct {
	Resource        string              `json:"resource"`
	Namespace       string              `json:"namespace"`
	Status          string              `json:"status"`
	Action          string              `json:"action,omitempty"`
	ProposedValue   string              `json:"proposedValue,omitempty"`
	Verification    *storagefix.Outcome `json:"verification,omitempty"`
	NonRollbackable bool                `json:"nonRollbackable,omitempty"`
	NoChangesMade   bool                `json:"noChangesMade"`
	Next            string              `json:"next,omitempty"`
}

func runPVCFix(ctx context.Context, out, errout io.Writer, o options, f analyzer.Finding) int {
	if o.pvcPolicy == "" {
		return runPVCFixRuntime(ctx, out, errout, o, nil, f)
	}
	client, err := kube.NewRequiredTypedClient(o.context, "policy-bound PVC storage repairs")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	runtime, err := storagefix.NewClient(client)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	return runPVCFixRuntime(ctx, out, errout, o, runtime, f)
}

func runPVCFixRuntime(ctx context.Context, out, errout io.Writer, o options, r storagefix.Runtime, f analyzer.Finding) int {
	if !isPVCResourceKind(f.ResourceKind) {
		fmt.Fprintln(errout, "--pvc-policy requires an exact PVC target")
		return 2
	}
	// Kubernetes discovery can surface this resource as either its canonical
	// kind or the short kubectl alias. The storage subsystem uses the canonical
	// kind in policy and identity checks.
	f.ResourceKind = "PersistentVolumeClaim"
	report := pvcRepairReport{Resource: "PersistentVolumeClaim/" + f.ResourceName, Namespace: f.Namespace, Status: f.Status, NoChangesMade: true}
	if o.pvcPolicy == "" {
		report.Status = "policy-required"
		report.Next = "Provide --pvc-policy <local-file> with the exact PVC, approved StorageClass assignment or bounded expansion, and maxStorage."
		return writePVCRepairReport(out, o, report, "Cause: "+pvcCause(f)+"\nNext: "+report.Next+"\nNo changes made.")
	}
	if r == nil {
		fmt.Fprintln(errout, "PVC policy is present but the typed Kubernetes runtime is unavailable")
		return 1
	}
	if o.servicePolicy != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.startupBaseline != "" || o.editPatch || o.repoPath != "" || o.gitops || hasHelmSourceContext(o) {
		fmt.Fprintln(errout, "PVC storage policy cannot be combined with workload/source patch modes")
		return 2
	}
	mode := strings.ToLower(strings.TrimSpace(o.delivery))
	if mode == "" {
		mode = "patch"
	}
	if mode != "patch" && mode != "cluster" {
		fmt.Fprintln(errout, "PVC policy delivery must be patch or cluster")
		return 2
	}
	if o.apply && o.visited["delivery"] && mode == "patch" {
		fmt.Fprintln(errout, "--apply conflicts with explicit --delivery=patch")
		return 2
	}
	authority, err := storagefix.LoadPolicy(o.pvcPolicy)
	if err != nil {
		fmt.Fprintf(errout, "PVC policy: %v\nNo changes made.\n", err)
		return 1
	}
	plan, err := storagefix.Prepare(ctx, r, authority, f)
	if err != nil {
		fmt.Fprintf(errout, "PVC repair requires review: %v\nNo changes made.\n", err)
		return 1
	}
	report.Action = plan.Action
	report.ProposedValue = plan.StorageClass
	if plan.Action == storagefix.ActionExpand {
		report.ProposedValue = plan.RequestedStorage
		report.NonRollbackable = true
	}
	apply := mode == "cluster" || o.apply && !o.visited["delivery"]
	if o.preview || !apply {
		message := pvcPreviewMessage(plan)
		if o.preview {
			message += "\nPreview only. No changes made."
		} else {
			message += "\nNo changes made. Use --delivery=cluster --yes to apply after verification."
		}
		report.Status = "preview"
		return writePVCRepairReport(out, o, report, message)
	}
	if plan.Action == storagefix.ActionExpand {
		fmt.Fprintln(errout, "Warning: PVC expansion cannot be rolled back automatically. Fixora will request the exact policy-approved capacity and report the observed result.")
	}
	receipt, err := storagefix.Verify(ctx, r, plan)
	if err != nil {
		fmt.Fprintf(errout, "PVC verification failed: %v\nNo changes made.\n", err)
		return 1
	}
	if !o.yes && !termui.Confirm("Apply the approved PVC storage change?", inputFor(o), out) {
		fmt.Fprintln(out, "Cancelled; no changes made.")
		return 0
	}
	applied, err := storagefix.Apply(ctx, r, plan, receipt)
	if err != nil {
		fmt.Fprintf(errout, "PVC apply failed: %v\n", err)
		return 1
	}
	report.NoChangesMade = false
	outcome, observeErr := storagefix.Observe(ctx, r, applied)
	if observeErr != nil {
		outcome = storagefix.Outcome{Status: "unknown", Message: "PVC change was applied, but post-apply observation failed."}
	}
	report.Verification = &outcome
	report.Status = outcome.Status
	code := writePVCRepairReport(out, o, report, pvcAppliedMessage(plan, outcome))
	if outcome.Status == "pending" && observeErr == nil {
		return code
	}
	if outcome.Status != "observed" || observeErr != nil {
		return 1
	}
	return code
}

func isPVCResourceKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "pvc", "pvcs", "persistentvolumeclaim", "persistentvolumeclaims":
		return true
	default:
		return false
	}
}

func writePVCRepairReport(out io.Writer, o options, report pvcRepairReport, text string) int {
	if o.output != "" && o.output != "text" {
		return output.Write(out, o.output, report)
	}
	fmt.Fprintln(out, text)
	return 0
}

func pvcPreviewMessage(p storagefix.Plan) string {
	if p.Action == storagefix.ActionSetStorageClass {
		return fmt.Sprintf("PVC repair proposal: set spec.storageClassName to %s\nIdentity: PVC UID %s, resourceVersion %s; StorageClass UID %s, resourceVersion %s.", p.StorageClass, p.PVCUID, p.PVCResourceVersion, p.StorageClassUID, p.StorageClassRV)
	}
	return fmt.Sprintf("PVC repair proposal: expand spec.resources.requests.storage to %s (maximum %s)\nIdentity: PVC UID %s, resourceVersion %s; PV %s UID %s; StorageClass %s UID %s.", p.RequestedStorage, p.MaximumStorage, p.PVCUID, p.PVCResourceVersion, p.PVName, p.PVUID, p.StorageClass, p.StorageClassUID)
}

func pvcAppliedMessage(p storagefix.Plan, outcome storagefix.Outcome) string {
	message := fmt.Sprintf("Applied %s to PVC %s/%s.\nVerification: %s", p.Action, p.Namespace, p.PVCName, outcome.Message)
	if p.Action == storagefix.ActionExpand {
		message += "\nThis expansion cannot be rolled back automatically."
	}
	return message
}

func pvcCause(f analyzer.Finding) string {
	if strings.TrimSpace(f.Summary) != "" {
		return f.Summary
	}
	return "PVC storage issue detected; verify the claim, StorageClass, PV, and consumer evidence before changing storage."
}
