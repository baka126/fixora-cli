package pdbfix

import (
	"context"
	"time"
)

func operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if _, ok := parent.Deadline(); ok {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, 3*time.Minute)
}
