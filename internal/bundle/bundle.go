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
	addJSON(tw, "graph.json", graph.Build(ctx, k, finding))
	addText(tw, "report.md", report.Markdown(finding))
	return nil
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
