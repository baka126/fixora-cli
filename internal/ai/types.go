package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type Adapter interface {
	Explain(context.Context, Request) (*analyzer.AIResult, error)
}

type Request struct {
	Provider     ProviderSpec
	Model        string
	BaseURL      string
	APIKey       string
	SystemPrompt string
	UserPrompt   string
	Contract     OutputContract
}

type OutputContract struct {
	Schema         map[string]any
	RequiredFields []string
}

type ErrorKind string

const (
	ErrorConfiguration  ErrorKind = "configuration"
	ErrorAuthentication ErrorKind = "authentication"
	ErrorUnsupported    ErrorKind = "unsupported"
	ErrorProvider       ErrorKind = "provider"
	ErrorRateLimit      ErrorKind = "rate-limit"
	ErrorTransient      ErrorKind = "transient"
	ErrorIncomplete     ErrorKind = "incomplete"
	ErrorContract       ErrorKind = "contract"
	ErrorResponseSize   ErrorKind = "response-size"
)

type ProviderError struct {
	Kind       ErrorKind
	StatusCode int
	Code       string
	Param      string
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "AI provider request failed"
	}
	return e.summary()
}

func (e *ProviderError) summary() string {
	var message string
	switch e.Kind {
	case ErrorConfiguration:
		message = "AI provider configuration is invalid"
	case ErrorAuthentication:
		message = "AI provider authentication failed"
	case ErrorUnsupported:
		message = "AI provider does not support the configured model or endpoint"
	case ErrorRateLimit:
		message = "AI provider rate limit reached"
	case ErrorTransient:
		message = "AI provider is temporarily unavailable"
	case ErrorIncomplete:
		message = "AI provider response was incomplete"
	case ErrorContract:
		message = "AI response did not satisfy Fixora's response contract"
	case ErrorResponseSize:
		message = "AI provider response exceeds the 1 MiB safety limit"
	default:
		message = "AI provider request failed"
	}
	if e.StatusCode > 0 {
		message += fmt.Sprintf(" (HTTP %d", e.StatusCode)
		if e.Code != "" {
			message += "; code=" + e.Code
		}
		if e.Param != "" {
			message += "; param=" + e.Param
		}
		message += ")"
	}
	return message
}

func (e *ProviderError) retryable() bool {
	return e != nil && (e.Kind == ErrorRateLimit || e.Kind == ErrorTransient)
}

func FailureSummary(err error) string {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.summary()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request deadline exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "request cancelled"
	}
	return "provider request failed"
}

func classifyProviderStatus(status int) ErrorKind {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorAuthentication
	case http.StatusNotFound:
		return ErrorUnsupported
	case http.StatusTooManyRequests:
		return ErrorRateLimit
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrorTransient
	default:
		return ErrorProvider
	}
}
