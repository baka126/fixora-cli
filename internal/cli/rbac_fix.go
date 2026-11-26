package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/rbacfix"
	"github.com/fixora/kubectl-fixora/internal/termui"
)

func runRBACRepair(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) != 1 || args[0] != "repair" {
		fmt.Fprintln(errout, "rbac repair requires exactly one subcommand")
		return 2
	}
	if o.rbacPolicy == "" {
		fmt.Fprintln(errout, "rbac repair requires --rbac-policy <local-file>")
		return 2
	}
	if o.grantID != "" {
		fmt.Fprintln(errout, "--grant is only supported by 'rbac revoke'")
		return 2
	}
	if !o.visited["namespace"] && !o.visited["n"] {
		fmt.Fprintln(errout, "rbac repair requires an explicit -n/--namespace matching the policy")
		return 2
	}
	if o.apply && o.preview {
		fmt.Fprintln(errout, "--apply and --preview cannot be combined")
		return 2
	}
	if o.yes && !o.apply {
		fmt.Fprintln(errout, "--yes requires --apply for rbac repair")
		return 2
	}
	if o.useAI || o.sourcePatch || o.gitops || o.autoFix || o.shadowVerify || o.repoPath != "" || o.delivery != "patch" || o.servicePolicy != "" || o.networkPolicyPolicy != "" || o.pvcPolicy != "" || o.pdbPolicy != "" || o.hpaPolicy != "" || o.startupBaseline != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.editPatch {
		fmt.Fprintln(errout, "rbac repair accepts only its local RBAC policy; AI, workload repair, shadow, and source-delivery options are not supported")
		return 2
	}
	a, err := rbacfix.LoadPolicy(o.rbacPolicy)
	if err != nil {
		fmt.Fprintf(errout, "RBAC policy: %v\n", err)
		return 1
	}
	if o.namespace != a.Namespace() {
		fmt.Fprintf(errout, "namespace mismatch: command uses %q but policy names %q\n", o.namespace, a.Namespace())
		return 2
	}
	k, err := kube.NewRequiredTypedClient(o.context, "policy-bound RBAC permission repair")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	r, err := rbacfix.NewClient(k)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	plan, err := rbacfix.Prepare(ctx, r, a)
	if err != nil {
		fmt.Fprintf(errout, "RBAC repair requires review: %v\nNo changes made.\n", err)
		return 1
	}
	if !o.apply {
		return writeRBACPreview(out, o, plan)
	}
	if o.output != "text" && !o.yes {
		fmt.Fprintln(errout, "machine-readable apply requires --yes so confirmation cannot corrupt structured output")
		return 2
	}
	if o.output == "text" {
		printRBACPlan(out, plan, "No changes made yet. Confirmation will create exactly these objects.")
	} else {
		printRBACPlan(errout, plan, "Applying only this approved policy; structured result will be written to stdout.")
	}
	if o.output == "text" && !o.yes && !termui.Confirm("Create this exact Role and RoleBinding for the named ServiceAccount?", inputFor(o), out) {
		fmt.Fprintln(out, "Cancelled; no changes made.")
		return 0
	}
	clusterID, err := kube.NewKubectl(o.context).ClusterID(ctx)
	if err != nil {
		fmt.Fprintf(errout, "RBAC repair requires a stable cluster identity: %v\n", err)
		return 1
	}
	store, err := rbacfix.NewGrantStore(clusterID)
	if err != nil {
		fmt.Fprintf(errout, "RBAC receipt store: %v\n", err)
		return 1
	}
	outcome, err := rbacfix.Apply(ctx, r, a, plan, store)
	if err != nil {
		fmt.Fprintf(errout, "RBAC repair failed: %v\nNo changes made.\n", err)
		return 1
	}
	if o.output != "text" {
		code := output.Write(out, o.output, outcome)
		if outcome.Status != "applied-and-authorized" {
			return 1
		}
		return code
	}
	fmt.Fprintf(out, "RBAC repair: %s\nNamespace: %s\n", outcome.Status, outcome.Namespace)
	if outcome.Role != "" {
		fmt.Fprintf(out, "Role: %s\n", outcome.Role)
	}
	if outcome.RoleBinding != "" {
		fmt.Fprintf(out, "RoleBinding: %s\n", outcome.RoleBinding)
	}
	if outcome.GrantID != "" {
		fmt.Fprintf(out, "Grant receipt: %s (%s)\nRevoke preview: kubectl fixora rbac revoke --grant %s -n %s\n", outcome.GrantID, outcome.ReceiptPath, outcome.GrantID, outcome.Namespace)
	}
	if outcome.Message != "" {
		fmt.Fprintln(out, outcome.Message)
	}
	if outcome.Status != "applied-and-authorized" {
		return 1
	}
	return 0
}

func writeRBACPreview(out io.Writer, o options, p *rbacfix.Plan) int {
	if o.output != "text" {
		return output.Write(out, o.output, map[string]any{"status": "ready-to-apply", "namespace": p.Namespace, "serviceAccount": p.ServiceAccount, "role": p.Role, "roleBinding": p.RoleBinding, "permissions": p.Requests, "decisions": p.Decisions, "caveats": p.Caveats, "noChangesMade": true})
	}
	printRBACPlan(out, p, "Preview only; no cluster changes made. Use --apply to create these exact objects after review.")
	return 0
}

func printRBACPlan(out io.Writer, p *rbacfix.Plan, footer string) {
	fmt.Fprintf(out, "RBAC repair preview (%s)\nServiceAccount: %s/%s\nRole: %s\nRoleBinding: %s\nPermissions:\n", p.Namespace, p.Namespace, p.ServiceAccount, p.Role.Name, p.RoleBinding.Name)
	for _, q := range p.Requests {
		target := q.Resource
		if q.APIGroup != "" {
			target = q.APIGroup + "/" + target
		}
		if q.Subresource != "" {
			target += "/" + q.Subresource
		}
		if q.Name != "" {
			target += " name=" + q.Name
		}
		fmt.Fprintf(out, "  - %s %s\n", q.Verb, target)
	}
	for _, c := range p.Caveats {
		fmt.Fprintf(out, "Caveat: %s\n", c)
	}
	fmt.Fprintln(out, footer)
}

func isRBACRepair(args []string) bool { return len(args) > 0 && strings.EqualFold(args[0], "repair") }

func isRBACRevoke(args []string) bool { return len(args) > 0 && strings.EqualFold(args[0], "revoke") }
func hasRBACGrantsPrefix(args []string) bool {
	return len(args) > 0 && strings.EqualFold(args[0], "grants")
}

func runRBACGrants(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(errout, "usage: kubectl fixora rbac grants <list|export|import>")
		return 2
	}
	switch strings.ToLower(args[1]) {
	case "list":
		return runRBACGrantsList(ctx, out, errout, o, args)
	case "export":
		return runRBACGrantsExport(ctx, out, errout, o, args)
	case "import":
		return runRBACGrantsImport(ctx, out, errout, o, args)
	default:
		fmt.Fprintln(errout, "usage: kubectl fixora rbac grants <list|export|import>")
		return 2
	}
}

func runRBACGrantsExport(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) != 2 || o.grantID == "" || o.outFile == "" || o.receiptFile != "" || o.apply || o.yes || o.rbacPolicy != "" || o.useAI || o.autoFix || o.sourcePatch || o.gitops || o.shadowVerify || o.editPatch || o.repoPath != "" {
		fmt.Fprintln(errout, "usage: kubectl fixora rbac grants export --grant <id> --out <path>")
		return 2
	}
	s, err := rbacStore(ctx, o)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	receipt, err := s.Get(o.grantID)
	if err != nil {
		fmt.Fprintf(errout, "grant receipt export: %v\n", err)
		return 1
	}
	if err := s.Export(o.grantID, o.outFile); err != nil {
		fmt.Fprintf(errout, "grant receipt export: %v\n", err)
		return 1
	}
	result := map[string]any{"status": "exported", "grantId": receipt.ID, "path": o.outFile, "namespace": receipt.Namespace, "serviceAccount": receipt.ServiceAccount, "role": receipt.Role.Name, "roleBinding": receipt.RoleBinding.Name, "sensitive": true}
	if o.output == "text" {
		fmt.Fprintf(out, "Exported grant %s to %s (private authorization metadata).\n", receipt.ID, o.outFile)
		return 0
	}
	return output.Write(out, o.output, result)
}

func runRBACGrantsImport(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) != 2 || o.receiptFile == "" || o.grantID != "" || o.outFile != "" || o.apply || o.yes || o.rbacPolicy != "" || o.useAI || o.autoFix || o.sourcePatch || o.gitops || o.shadowVerify || o.editPatch || o.repoPath != "" {
		fmt.Fprintln(errout, "usage: kubectl fixora rbac grants import --file <path>")
		return 2
	}
	s, err := rbacStore(ctx, o)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	kclient, err := kube.NewRequiredTypedClient(o.context, "RBAC grant receipt import")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	runtime, err := rbacfix.NewClient(kclient)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	grant, err := rbacfix.ImportGrant(ctx, runtime, s, o.receiptFile)
	if err != nil {
		fmt.Fprintf(errout, "grant receipt import: %v\nNo local receipt was saved.\n", err)
		return 1
	}
	result := map[string]any{"status": "imported", "grant": grant, "clusterChanges": false}
	if o.output == "text" {
		fmt.Fprintf(out, "Imported grant %s for ServiceAccount/%s in namespace %s. No cluster changes made.\n", grant.ID, grant.ServiceAccount, grant.Namespace)
		return 0
	}
	return output.Write(out, o.output, result)
}

func rbacStore(ctx context.Context, o options) (*rbacfix.GrantStore, error) {
	id, err := kube.NewKubectl(o.context).ClusterID(ctx)
	if err != nil {
		return nil, err
	}
	return rbacfix.NewGrantStore(id)
}

func runRBACGrantsList(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) != 2 || !strings.EqualFold(args[1], "list") || o.apply || o.yes || o.rbacPolicy != "" || o.grantID != "" || o.receiptFile != "" || o.outFile != "" || o.useAI {
		fmt.Fprintln(errout, "usage: kubectl fixora rbac grants list")
		return 2
	}
	s, err := rbacStore(ctx, o)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	items, err := s.List()
	if err != nil {
		fmt.Fprintf(errout, "grant receipts: %v\n", err)
		return 1
	}
	if o.output != "text" {
		return output.Write(out, o.output, map[string]any{"grants": items, "source": "local-receipts-only", "clusterId": "redacted"})
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "No local Fixora grant receipts for this cluster.")
		return 0
	}
	fmt.Fprintln(out, "Local Fixora grants (cluster API not queried):")
	for _, g := range items {
		fmt.Fprintf(out, "%s  %s/%s  ServiceAccount/%s  RoleBinding/%s  Role/%s  %s\n", g.ID, g.Namespace, g.Namespace, g.ServiceAccount, g.RoleBinding, g.Role, g.CreatedAt.Format("2006-01-02 15:04 UTC"))
	}
	return 0
}

func runRBACRevoke(ctx context.Context, out, errout io.Writer, o options, args []string) int {
	if len(args) != 1 || o.grantID == "" || !o.visited["namespace"] && !o.visited["n"] {
		fmt.Fprintln(errout, "usage: kubectl fixora rbac revoke --grant <id> -n <namespace> [--apply]")
		return 2
	}
	if o.rbacPolicy != "" || o.useAI || o.sourcePatch || o.gitops || o.autoFix || o.shadowVerify || o.repoPath != "" || o.delivery != "patch" || o.servicePolicy != "" || o.networkPolicyPolicy != "" || o.pvcPolicy != "" || o.pdbPolicy != "" || o.hpaPolicy != "" || o.startupBaseline != "" || o.repairBaseline != "" || o.repairPolicy != "" || o.editPatch {
		fmt.Fprintln(errout, "rbac revoke accepts only a grant receipt, namespace, and confirmation flags")
		return 2
	}
	if o.apply && o.preview || o.yes && !o.apply {
		fmt.Fprintln(errout, "invalid confirmation flags: --yes requires --apply, and --preview cannot be combined with --apply")
		return 2
	}
	if o.apply && o.output != "text" && !o.yes {
		fmt.Fprintln(errout, "machine-readable apply requires --yes")
		return 2
	}
	s, err := rbacStore(ctx, o)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	kclient, err := kube.NewRequiredTypedClient(o.context, "receipt-backed RBAC grant revocation")
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	r, err := rbacfix.NewClient(kclient)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	p, err := rbacfix.PrepareRevoke(ctx, r, s, o.grantID, o.namespace)
	if err != nil {
		fmt.Fprintf(errout, "RBAC revoke requires review: %v\nNo changes made.\n", err)
		return 1
	}
	if !o.apply {
		if o.output != "text" {
			return output.Write(out, o.output, map[string]any{"status": "ready-to-revoke", "grantId": p.Receipt.ID, "namespace": p.Receipt.Namespace, "serviceAccount": p.Receipt.ServiceAccount, "roleBinding": p.Receipt.RoleBinding.Name, "role": p.Receipt.Role.Name, "bindingPresent": p.BindingPresent, "rolePresent": p.RolePresent, "sharedRole": p.SharedRole, "serviceAccountChanged": p.ServiceAccountChanged, "noChangesMade": true})
		}
		fmt.Fprintf(out, "RBAC grant revoke preview\nGrant: %s\nNamespace: %s\nServiceAccount: %s\nRoleBinding: %s (present=%t)\nRole: %s (present=%t; shared=%t)\n", p.Receipt.ID, p.Receipt.Namespace, p.Receipt.ServiceAccount, p.Receipt.RoleBinding.Name, p.BindingPresent, p.Receipt.Role.Name, p.RolePresent, p.SharedRole)
		if p.ServiceAccountChanged {
			fmt.Fprintln(out, "Note: ServiceAccount is missing or has a different UID; the exact recorded RoleBinding can still be removed.")
		}
		fmt.Fprintln(out, "Preview only; no changes made. Use --apply to revoke this exact grant.")
		return 0
	}
	if o.output == "text" && !o.yes && !termui.Confirm("Delete only the exact receipt-bound RoleBinding and unshared Role?", inputFor(o), out) {
		fmt.Fprintln(out, "Cancelled; no changes made.")
		return 0
	}
	result, err := rbacfix.Revoke(ctx, r, s, p)
	if err != nil {
		fmt.Fprintf(errout, "RBAC revoke failed: %v\n", err)
		return 1
	}
	if o.output != "text" {
		code := output.Write(out, o.output, result)
		if result.Status != "grant-revoked-and-denied" && result.Status != "grant-revoked-access-remains" && result.Status != "grant-revoked-role-retained" && result.Status != "revoke-complete-receipt-retained" {
			return 1
		}
		return code
	}
	fmt.Fprintf(out, "RBAC grant revoke: %s\nNamespace: %s\n%s\n", result.Status, result.Namespace, result.Message)
	if result.Status == "grant-revoked-and-denied" || result.Status == "grant-revoked-access-remains" || result.Status == "grant-revoked-role-retained" || result.Status == "revoke-complete-receipt-retained" {
		return 0
	}
	return 1
}
