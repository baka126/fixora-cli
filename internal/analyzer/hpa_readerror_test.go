package analyzer

import (
	"context"
	"errors"
	"testing"
)

type hpaTargetErrorReader struct {
	fakeReader
	targetErr error
}

func (r hpaTargetErrorReader) GetResource(context.Context, string, string) (map[string]any, error) {
	return nil, r.targetErr
}

func TestHPATargetReadErrorIsNotReportedAsMissing(t *testing.T) {
	hpa := map[string]any{
		"metadata": map[string]any{"name": "api", "namespace": "prod"},
		"spec":     map[string]any{"scaleTargetRef": map[string]any{"kind": "Deployment", "name": "api"}},
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"forbidden", errors.New("deployments.apps is forbidden"), "ScaleTargetUnreadable"},
		{"unavailable", errors.New("connection refused"), "ScaleTargetUnreadable"},
		{"missing", errors.New("deployments.apps api NotFound"), "MissingScaleTarget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := hpaTargetErrorReader{fakeReader: fakeReader{items: map[string][]map[string]any{"hpa": {hpa}}}, targetErr: tc.err}
			ctx := NewScanContext(context.Background(), reader, Options{Namespace: "prod"})
			findings, err := New(reader, Options{Namespace: "prod"}).analyzeHPATargets(ctx)
			if err != nil || len(findings) != 1 || findings[0].Status != tc.want {
				t.Fatalf("findings=%#v err=%v want=%s", findings, err, tc.want)
			}
		})
	}
}
