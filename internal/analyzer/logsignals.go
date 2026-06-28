package analyzer

import "strings"

// signalClass distinguishes how a matched log signal should be acted on.
type signalClass int

const (
	// classPatch: a safe-by-default workload patch exists (existing arch/perm signals).
	classPatch signalClass = iota
	// classAdvise: review-only diagnosis; the cause is typically external/operator-owned.
	classAdvise
	// classNoMutate: code-level fault; explicitly must not patch the workload spec.
	classNoMutate
)

// logSignal is one deterministic log-pattern → diagnosis mapping. The table
// order in logSignals encodes precedence: the first matching signal wins.
type logSignal struct {
	status   string
	summary  string
	category string
	class    signalClass
	match    func(folded string) bool
	rec      Recommendation
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// logSignals is ordered most-specific/most-certain cause first. ApplicationPanic
// is last so a specific external cause appearing above a stack trace wins (the
// panic is usually the symptom, not the root cause).
var logSignals = []logSignal{
	{
		status:   "ExecFormatError",
		summary:  "Container failed to execute due to an architecture mismatch (exec format error).",
		category: "runtime",
		class:    classPatch,
		match:    func(s string) bool { return strings.Contains(s, "exec format error") },
		rec: Recommendation{
			Title:         "Deploy matching image architecture",
			Description:   "The container image architecture does not match the CPU architecture of the node. Rebuild the image for this platform (e.g., using docker buildx for linux/arm64 or linux/amd64) or schedule the pod on a node with a matching CPU architecture.",
			PatchType:     "fix-architecture",
			SafeByDefault: true,
		},
	},
	{
		status:   "PermissionDenied",
		summary:  "Container execution failed due to insufficient file or system permissions.",
		category: "security",
		class:    classPatch,
		match:    func(s string) bool { return strings.Contains(s, "permission denied") },
		rec: Recommendation{
			Title:         "Adjust securityContext or file permissions",
			Description:   "Check runAsUser, fsGroup, readOnlyRootFilesystem, and volume/mount directory permissions.",
			PatchType:     "security",
			SafeByDefault: false,
		},
	},
	{
		status:   "DiskFull",
		summary:  "The container ran out of disk space.",
		category: "storage",
		class:    classAdvise,
		match:    func(s string) bool { return containsAny(s, "no space left on device", "disk quota exceeded") },
		rec: Recommendation{
			Title:       "Free or expand storage",
			Description: "A volume or the node ephemeral storage is full. Check the PVC capacity and usage, clear or rotate large files, or expand the volume at the source manifest. fixora does not auto-resize storage.",
		},
	},
	{
		status:   "DNSResolutionFailed",
		summary:  "DNS resolution failed inside the pod.",
		category: "networking",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "no such host", "could not resolve host", "temporary failure in name resolution", "server misbehaving")
		},
		rec: Recommendation{
			Title:       "Verify DNS and Service name",
			Description: "The pod could not resolve a hostname. Confirm the target Service name/namespace, CoreDNS health, and that any NetworkPolicy permits egress to kube-dns.",
		},
	},
	{
		status:   "TLSHandshakeError",
		summary:  "A TLS handshake or certificate validation failed.",
		category: "networking",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "tls: handshake failure", "x509:", "certificate signed by unknown authority", "certificate has expired")
		},
		rec: Recommendation{
			Title:       "Check certificates and trust chain",
			Description: "TLS verification failed. Check certificate validity/expiry, the CA trust bundle mounted into the pod, and SNI/hostname matching. Rotate or re-issue the certificate at the source. fixora does not mint certificates.",
		},
	},
	{
		status:   "AuthenticationFailed",
		summary:  "The application was rejected by an upstream authentication check.",
		category: "security",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "authentication failed", "invalid credentials", "password authentication failed", "401 unauthorized", "access denied")
		},
		rec: Recommendation{
			Title:       "Verify credentials and secret references",
			Description: "An upstream rejected the workload's credentials. Confirm the referenced Secret holds current values and that the username/token/role is valid. Update the credential at the source; fixora does not write secret values.",
		},
	},
	{
		status:   "DatabaseUnreachable",
		summary:  "The container could not reach its database dependency.",
		category: "dependency",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "could not connect to server", "econnrefused") ||
				(strings.Contains(s, "connection refused") && containsAny(s, "postgres", "mysql", "mongo", "redis"))
		},
		rec: Recommendation{
			Title:       "Check the database dependency",
			Description: "The workload cannot connect to its database. Verify the dependency Service and endpoints are ready, the connection string/port is correct, and any NetworkPolicy permits egress. fixora does not patch an external dependency.",
		},
	},
	{
		status:   "DependencyTimeout",
		summary:  "A network call to a dependency timed out.",
		category: "dependency",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "context deadline exceeded", "i/o timeout", "request timed out") ||
				containsAll(s, "dial tcp", "timeout")
		},
		rec: Recommendation{
			Title:       "Investigate the slow or unreachable dependency",
			Description: "A call to an upstream dependency timed out. Check that the dependency is healthy and reachable, review client timeout settings, and confirm NetworkPolicy egress. fixora does not patch an external dependency.",
		},
	},
	{
		status:   "ConfigParseError",
		summary:  "The application failed to parse its configuration.",
		category: "config",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAny(s, "yaml: line", "json: cannot unmarshal", "failed to parse config", "invalid configuration", "toml:")
		},
		rec: Recommendation{
			Title:       "Fix the malformed configuration",
			Description: "The workload could not parse its configuration. Correct the malformed ConfigMap/file at the source manifest and roll out the change. fixora does not edit configuration values.",
		},
	},
	{
		status:   "MissingEnvVar",
		summary:  "A required environment variable was not set.",
		category: "config",
		class:    classAdvise,
		match: func(s string) bool {
			return containsAll(s, "environment variable", "not set") || containsAny(s, "missing required env", "is not defined")
		},
		rec: Recommendation{
			Title:       "Supply the missing environment variable",
			Description: "A required environment variable is unset. Add it via the workload's env/envFrom (ConfigMap or Secret) at the source manifest. fixora does not invent configuration values.",
		},
	},
	{
		status:   "ApplicationPanic",
		summary:  "The application crashed with a panic or unhandled exception (code-level fault).",
		category: "runtime",
		class:    classNoMutate,
		match: func(s string) bool {
			return containsAny(s, "panic:", "goroutine ", "traceback (most recent call last)", "fatal error:", "segmentation fault", "unhandled exception")
		},
		rec: Recommendation{
			Title:       "Fix the application code",
			Description: "The application crashed with a panic/stack trace — a code-level fault. Fix it in the application source or image and rebuild. Do not mutate the workload spec; a Kubernetes patch will not resolve a code bug.",
		},
	},
}

// firstSignal returns the first signal whose match predicate accepts folded
// (already lower-cased) text.
func firstSignal(folded string) (logSignal, bool) {
	for _, sig := range logSignals {
		if sig.match(folded) {
			return sig, true
		}
	}
	return logSignal{}, false
}

// classifyLogSignal classifies current logs first (authoritative, most recent),
// falling back to previous logs only if current matched nothing. recurring is
// true only when the same status matches in BOTH current and previous logs.
// ok is false when neither log matched any signal.
func classifyLogSignal(current, previous string) (logSignal, bool, bool) {
	curSig, curOK := firstSignal(strings.ToLower(current))
	if !curOK {
		if prevSig, prevOK := firstSignal(strings.ToLower(previous)); prevOK {
			return prevSig, false, true
		}
		return logSignal{}, false, false
	}
	prevSig, prevOK := firstSignal(strings.ToLower(previous))
	recurring := prevOK && prevSig.status == curSig.status
	return curSig, recurring, true
}
