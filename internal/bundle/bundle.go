package bundle

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/graph"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/report"
)

func Write(ctx context.Context, k kube.Kubectl, out string, finding analyzer.Finding, plan fix.Plan) error {
	return WriteProfile(ctx, k, out, finding, plan, "incident")
}

func WriteProfile(ctx context.Context, k kube.Kubectl, out string, finding analyzer.Finding, plan fix.Plan, profile string) error {
	if out == "" {
		out = "fixora-bundle.tgz"
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	addJSON(tw, "finding.json", finding)
	addJSON(tw, "plan.json", plan)
	addText(tw, "report.md", report.Markdown(finding))
	switch profile {
	case "network":
		addJSON(tw, "graph.json", graph.Build(ctx, k, finding))
		addJSON(tw, "services.json", resourceItems(ctx, k, finding.Namespace, "services"))
		addJSON(tw, "endpoints.json", resourceItems(ctx, k, finding.Namespace, "endpoints"))
		addJSON(tw, "ingresses.json", resourceItems(ctx, k, finding.Namespace, "ingresses"))
	case "storage":
		addJSON(tw, "pvcs.json", resourceItems(ctx, k, finding.Namespace, "pvc"))
		addJSON(tw, "storageclasses.json", resourceItems(ctx, k, "", "storageclasses"))
	case "security":
		addJSON(tw, "events.json", events(ctx, k, finding.Namespace))
		addJSON(tw, "policyreports.json", resourceItems(ctx, k, finding.Namespace, "policyreports.wgpolicyk8s.io"))
		addJSON(tw, "networkpolicies.json", resourceItems(ctx, k, finding.Namespace, "networkpolicies"))
	default:
		addJSON(tw, "graph.json", graph.Build(ctx, k, finding))
		addJSON(tw, "events.json", events(ctx, k, finding.Namespace))
	}
	return nil
}

func resourceItems(ctx context.Context, k kube.Kubectl, namespace, resource string) any {
	items, err := k.GetResourceItems(ctx, namespace, namespace == "", resource)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	return items
}

func events(ctx context.Context, k kube.Kubectl, namespace string) any {
	items, err := k.GetEvents(ctx, namespace)
	if err != nil {
		return map[string]string{"error": err.Error()}
	}
	return items
}

func addJSON(tw *tar.Writer, name string, value any) {
	data, _ := json.MarshalIndent(value, "", "  ")
	addText(tw, name, string(data))
}

func addText(tw *tar.Writer, name, value string) {
	data := []byte(value)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()})
	_, _ = tw.Write(data)
}
