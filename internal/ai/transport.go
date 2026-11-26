package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

const (
	maxAIResponseBytes    = 1 << 20
	maxProviderErrorBytes = 64 << 10
	maxRetryAfter         = 30 * time.Second
)

var safeProviderDetail = regexp.MustCompile(`^[A-Za-z0-9_.:\[\]-]{1,80}$`)

func readProviderResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProviderErrorBytes))
		delay := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return nil, providerErrorFromResponse(resp.StatusCode, delay, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAIResponseBytes {
		return nil, &ProviderError{Kind: ErrorResponseSize}
	}
	return data, nil
}

func providerErrorFromResponse(status int, retryAfter time.Duration, body []byte) error {
	err := &ProviderError{Kind: classifyProviderStatus(status), StatusCode: status, RetryAfter: retryAfter}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) == nil {
		if nested, ok := envelope["error"].(map[string]any); ok {
			err.Code = firstSafeDetail(asString(nested["code"]), asString(nested["type"]))
			err.Param = safeDetail(asString(nested["param"]))
		} else {
			err.Code = safeDetail(asString(envelope["code"]))
			err.Param = safeDetail(asString(envelope["param"]))
		}
	}
	return err
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

func firstSafeDetail(values ...string) string {
	for _, value := range values {
		if safe := safeDetail(value); safe != "" {
			return safe
		}
	}
	return ""
}

func safeDetail(value string) string {
	value = strings.TrimSpace(value)
	if !safeProviderDetail.MatchString(value) {
		return ""
	}
	return value
}

func retryProviderCall(ctx context.Context, call func() (*analyzer.AIResult, error)) (*analyzer.AIResult, error) {
	var result *analyzer.AIResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result, err = call()
		var providerErr *ProviderError
		if err == nil || !errors.As(err, &providerErr) || !providerErr.retryable() || attempt == 2 {
			return result, err
		}
		fallback := time.Duration(200<<attempt) * time.Millisecond
		delay := boundedRetryDelay(providerErr.RetryAfter, fallback)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return result, err
}

func boundedRetryDelay(serverDelay, fallback time.Duration) time.Duration {
	delay := fallback
	if serverDelay > delay {
		delay = serverDelay
	}
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		if seconds > int64((1<<63-1)/int64(time.Second)) {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
