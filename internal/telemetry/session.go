package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrEndpointRequired = errors.New("FIXORA_TELEMETRY_URL is required")
	ErrEndpointInvalid  = errors.New("telemetry endpoint must be an HTTPS URL without credentials or query parameters")
	ErrUploadFailed     = errors.New("telemetry collector did not accept the report")
)

type Report struct {
	SchemaVersion int    `json:"schemaVersion"`
	ClientVersion string `json:"clientVersion"`
	FailureType   string `json:"failureType"`
	Stage         string `json:"stage"`
	Verdict       string `json:"verdict"`
	Retries       int    `json:"retries"`
	Parity        *int   `json:"parity,omitempty"`
	Applied       bool   `json:"applied"`
}

// Session holds only the small, identifier-free fields allowed in a telemetry
// report. Resource names, namespaces, images, patches, logs, and AI content are
// deliberately absent from its data model.
type Session struct {
	report   Report
	endpoint string
	token    string
}

func NewSession(clientVersion, failureType, endpoint, token string) *Session {
	return &Session{
		report: Report{
			SchemaVersion: 1,
			ClientVersion: clientVersion,
			FailureType:   normalizeFailureType(failureType),
			Stage:         "diagnosis",
			Verdict:       "not_run",
		},
		endpoint: strings.TrimSpace(endpoint),
		token:    token,
	}
}

func (s *Session) SetFailureType(failureType string) {
	if s == nil {
		return
	}
	s.report.FailureType = normalizeFailureType(failureType)
}

func (s *Session) SetStage(stage string) {
	if s == nil {
		return
	}
	switch stage {
	case "diagnosis", "candidate", "verification", "delivery":
		s.report.Stage = stage
	}
}

func (s *Session) SetVerification(verdict string, attempts int, parity *int) {
	if s == nil {
		return
	}
	s.report.Stage = "verification"
	switch verdict {
	case "verified", "failed", "inconclusive", "blocked":
		s.report.Verdict = verdict
	default:
		s.report.Verdict = "blocked"
	}
	if attempts > 1 {
		s.report.Retries = attempts - 1
	}
	if parity != nil && *parity >= 0 && *parity <= 100 {
		value := *parity
		s.report.Parity = &value
	}
}

func (s *Session) MarkApplied() {
	if s == nil {
		return
	}
	s.report.Stage = "delivery"
	s.report.Applied = true
}

func (s *Session) Report() Report {
	if s == nil {
		return Report{}
	}
	return s.report
}

func (s *Session) Send(ctx context.Context, client *http.Client) error {
	if s == nil {
		return nil
	}
	endpoint, err := url.Parse(s.endpoint)
	if s.endpoint == "" {
		return ErrEndpointRequired
	}
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return ErrEndpointInvalid
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copy := *client
		copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	body, err := json.Marshal(s.report)
	if err != nil {
		return ErrUploadFailed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return ErrEndpointInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fixora-telemetry/1")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUploadFailed
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w (HTTP %d)", ErrUploadFailed, resp.StatusCode)
	}
	return nil
}

func normalizeFailureType(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(s, "oom"):
		return "oom"
	case strings.Contains(s, "crashloop"):
		return "crash_loop"
	case strings.Contains(s, "imagepull") || strings.Contains(s, "errimagepull"):
		return "image_pull"
	case strings.Contains(s, "probe"):
		return "probe"
	case strings.Contains(s, "config") || strings.Contains(s, "secret"):
		return "configuration"
	case strings.Contains(s, "pending") || strings.Contains(s, "unschedul") || strings.Contains(s, "replicas"):
		return "scheduling_or_capacity"
	case strings.Contains(s, "service") || strings.Contains(s, "endpoint") || strings.Contains(s, "database") || strings.Contains(s, "dns"):
		return "dependency_or_routing"
	case strings.Contains(s, "permission") || strings.Contains(s, "forbidden") || strings.Contains(s, "security"):
		return "permission_or_security"
	case s == "":
		return "unknown"
	case s == "healthy" || s == "ok":
		return "none"
	default:
		return "other"
	}
}
