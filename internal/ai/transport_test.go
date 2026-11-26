package ai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestReadProviderResponseSanitizesErrorBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"secret prompt and key","code":"invalid_request_error","param":"temperature"}}`)),
	}
	_, err := readProviderResponse(resp)
	if err == nil {
		t.Fatal("expected provider error")
	}
	summary := FailureSummary(err)
	if !strings.Contains(summary, "HTTP 400") || !strings.Contains(summary, "invalid_request_error") || !strings.Contains(summary, "temperature") {
		t.Fatalf("missing safe error details: %q", summary)
	}
	if strings.Contains(summary, "secret prompt") || strings.Contains(summary, " key") {
		t.Fatalf("provider error leaked raw response: %q", summary)
	}
}

func TestRetryProviderCallDoesNotRetryPermanentError(t *testing.T) {
	calls := 0
	_, err := retryProviderCall(context.Background(), func() (*analyzer.AIResult, error) {
		calls++
		return nil, &ProviderError{Kind: ErrorProvider, StatusCode: http.StatusBadRequest}
	})
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v, want one non-retried call", calls, err)
	}
}

func TestBoundedRetryDelayCapsProviderHeader(t *testing.T) {
	if got := boundedRetryDelay(2*time.Minute, 200*time.Millisecond); got != maxRetryAfter {
		t.Fatalf("boundedRetryDelay = %s, want %s", got, maxRetryAfter)
	}
}

func TestRetryProviderCallStopsWhenContextExpires(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := retryProviderCall(ctx, func() (*analyzer.AIResult, error) {
		calls++
		return nil, &ProviderError{Kind: ErrorRateLimit, StatusCode: http.StatusTooManyRequests, RetryAfter: time.Second}
	})
	if err != context.DeadlineExceeded || calls != 1 {
		t.Fatalf("calls=%d err=%v, want one call and context deadline", calls, err)
	}
}
