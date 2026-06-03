package ai

import (
	"context"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type Provider interface {
	Explain(ctx context.Context, finding analyzer.Finding) (*analyzer.AIResult, error)
}
