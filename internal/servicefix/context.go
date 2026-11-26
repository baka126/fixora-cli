package servicefix

import (
	"context"
	"time"
)

// Bound API work before its first read; an explicit parent deadline is retained.
func operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 3*time.Minute)
}
