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
	"github.com/fixora/kubectl-fixora/internal/redact"
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
	if err := addJSON(tw, "finding.json", finding); err != nil {
		return err
	}
	if err := addJSON(tw, "plan.json", plan); err != nil {
		return err
	}
	if err := addText(tw, "report.md", report.Markdown(finding)); err != nil {
		return err
	}
	switch profile {
	case "network":
		if err := addJSON(tw, "graph.json", graph.Build(ctx, k, finding)); err != nil { return err }
		if err := addJSON(tw, "services.json", resourceItems(ctx, k, finding.Namespace, "services")); err != nil { return err }
		if err := addJSON(tw, "endpoints.json", resourceItems(ctx, k, finding.Namespace, "endpoints")); err != nil { return err }
		if err := addJSON(tw, "ingresses.json", resourceItems(ctx, k, finding.Namespace, "ingresses")); err != nil { return err }
	case "storage":
		if err := addJSON(tw, "pvcs.json", resourceItems(ctx, k, finding.Namespace, "pvc")); err != nil { return err }
		if err := addJSON(tw, "storageclasses.json", resourceItems(ctx, k, "", "storageclasses")); err != nil { return err }
	case "security":
		if err := addJSON(tw, "events.json", events(ctx, k, finding.Namespace)); err != nil { return err }
		if err := addJSON(tw, "policyreports.json", resourceItems(ctx, k, finding.Namespace, "policyreports.wgpolicyk8s.io")); err != nil { return err }
		if err := addJSON(tw, "networkpolicies.json", resourceItems(ctx, k, finding.Namespace, "networkpolicies")); err != nil { return err }
	default:
		if err := addJSON(tw, "graph.json", graph.Build(ctx, k, finding)); err != nil { return err }
		if err := addJSON(tw, "events.json", events(ctx, k, finding.Namespace)); err != nil { return err }
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

func addJSON(tw *tar.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return addText(tw, name, string(data))
}

func addText(tw *tar.Writer, name, value string) error {
	data := []byte(redact.Text(value))
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
