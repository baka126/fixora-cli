package analyzer

import (
	"strings"
	"testing"
)

func TestClassifyLogSignalPerSignal(t *testing.T) {
	cases := []struct {
		name       string
		log        string
		wantStatus string
		wantClass  signalClass
	}{
		{"execformat", "standard_init_linux.go: exec format error", "ExecFormatError", classPatch},
		{"permission", "open /data/x: Permission denied", "PermissionDenied", classPatch},
		{"diskfull", "write /var/log/app.log: no space left on device", "DiskFull", classAdvise},
		{"dns", "dial tcp: lookup api.svc: no such host", "DNSResolutionFailed", classAdvise},
		{"tls", "x509: certificate signed by unknown authority", "TLSHandshakeError", classAdvise},
		{"auth", "login failed: invalid credentials", "AuthenticationFailed", classAdvise},
		{"db", "could not connect to server: Connection refused", "DatabaseUnreachable", classAdvise},
		{"db-engine", "dial tcp 10.0.0.5:5432: connection refused (postgres)", "DatabaseUnreachable", classAdvise},
		{"timeout", "rpc error: context deadline exceeded", "DependencyTimeout", classAdvise},
		{"config", "yaml: line 4: did not find expected key", "ConfigParseError", classAdvise},
		{"missingenv", "required environment variable DB_HOST not set", "MissingEnvVar", classAdvise},
		{"panic", "panic: runtime error: invalid memory address", "ApplicationPanic", classNoMutate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sig, _, ok := classifyLogSignal(c.log, "")
			if !ok {
				t.Fatalf("expected a match for %q", c.log)
			}
			if sig.status != c.wantStatus {
				t.Fatalf("status = %q, want %q", sig.status, c.wantStatus)
			}
			if sig.class != c.wantClass {
				t.Fatalf("class = %d, want %d", sig.class, c.wantClass)
			}
		})
	}
}

func TestClassifyLogSignalPrecedenceSpecificBeatsPanic(t *testing.T) {
	log := "could not connect to server: Connection refused\npanic: cannot reach db\ngoroutine 1 [running]:"
	sig, _, ok := classifyLogSignal(log, "")
	if !ok || sig.status != "DatabaseUnreachable" {
		t.Fatalf("specific cause must beat panic catch-all; got %q ok=%v", sig.status, ok)
	}
}

func TestClassifyLogSignalBarePanic(t *testing.T) {
	sig, _, ok := classifyLogSignal("panic: runtime error: index out of range", "")
	if !ok || sig.status != "ApplicationPanic" {
		t.Fatalf("bare panic must classify as ApplicationPanic; got %q ok=%v", sig.status, ok)
	}
}

func TestClassifyLogSignalRecurrence(t *testing.T) {
	cur := "panic: nil pointer dereference"
	prev := "panic: nil pointer dereference"
	_, recurring, ok := classifyLogSignal(cur, prev)
	if !ok || !recurring {
		t.Fatalf("same signal in both logs must be recurring; recurring=%v ok=%v", recurring, ok)
	}
}

func TestClassifyLogSignalRegressionNotRecurring(t *testing.T) {
	_, recurring, ok := classifyLogSignal("could not connect to server", "")
	if !ok || recurring {
		t.Fatalf("signal only in current must not be recurring; recurring=%v ok=%v", recurring, ok)
	}
}

func TestClassifyLogSignalFallsBackToPrevious(t *testing.T) {
	sig, recurring, ok := classifyLogSignal("", "x509: certificate has expired")
	if !ok || sig.status != "TLSHandshakeError" {
		t.Fatalf("must fall back to previous logs; got %q ok=%v", sig.status, ok)
	}
	if recurring {
		t.Fatalf("fallback-from-previous must not be flagged recurring")
	}
}

func TestClassifyLogSignalNoMatch(t *testing.T) {
	if _, _, ok := classifyLogSignal("server started on :8080", "ready"); ok {
		t.Fatalf("unrelated logs must not match")
	}
}

func TestLogSignalStatusesDoNotCollideWithPlannerKeys(t *testing.T) {
	// Mirrors the strings.Contains keys in fix.BuildPlan's switch. A new signal
	// status that is a substring of, or contains, any of these would silently
	// inherit a (possibly mutating) planner strategy instead of falling through
	// to the blocked default branch.
	plannerKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	for _, sig := range logSignals {
		if sig.status == "ExecFormatError" || sig.status == "PermissionDenied" {
			continue // existing PATCH signals intentionally match their keys
		}
		sLow := strings.ToLower(sig.status)
		for _, key := range plannerKeys {
			kLow := strings.ToLower(key)
			if strings.Contains(sLow, kLow) || strings.Contains(kLow, sLow) {
				t.Fatalf("signal status %q collides with planner key %q", sig.status, key)
			}
		}
	}
}

func TestClassifyLogSignalPlainConnectionRefusedIsNotDatabase(t *testing.T) {
	// A bare "connection refused" with no DB engine token must NOT classify as
	// DatabaseUnreachable. This locks the deliberate decision that keeps the
	// status clear of BuildPlan's ConnectionRefused switch key.
	sig, _, ok := classifyLogSignal("dial tcp 10.0.0.5:9000: connect: connection refused", "")
	if ok && sig.status == "DatabaseUnreachable" {
		t.Fatalf("plain connection refused must not classify as DatabaseUnreachable; got %q", sig.status)
	}
}
