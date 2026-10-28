package shadow

import (
	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type DeliveryMode string

type VerificationStatus string

const (
	VerificationVerified     VerificationStatus = "verified"
	VerificationFailed       VerificationStatus = "failed"
	VerificationInconclusive VerificationStatus = "inconclusive"
	VerificationBlocked      VerificationStatus = "blocked"
)

const (
	DeliveryPatch   DeliveryMode = "patch"
	DeliveryCluster DeliveryMode = "cluster"
	DeliveryPR      DeliveryMode = "pr"
)

type Request struct {
	Namespace   string
	Resource    string
	Patch       string
	Finding     analyzer.Finding
	Plan        fix.Plan
	Timeout     time.Duration
	Retries     int
	Keep        bool
	Egress      string
	ProbeImage  string
	Delivery    DeliveryMode
	RepoPath    string
	Branch      string
	PRBase      string
	PRTitle     string
	OutFile     string
	ApplyDryRun bool
	Redact      bool
	AI          ai.Provider
}

type Result struct {
	PolicyReceipt      *repairpolicy.Receipt `json:"-"`
	PolicySummary      *repairpolicy.Summary `json:"policySummary,omitempty"`
	LiveVerification   string                `json:"liveVerification,omitempty"`
	LiveSummary        string                `json:"liveSummary,omitempty"`
	Verified           bool                  `json:"verified"`
	Status             VerificationStatus    `json:"status"`
	IsolationNamespace string                `json:"isolationNamespace,omitempty"`
	Isolation          *IsolationReport      `json:"isolation,omitempty"`
	Parity             int                   `json:"parity"`
	Resource           string                `json:"resource"`
	Namespace          string                `json:"namespace"`
	CloneName          string                `json:"cloneName,omitempty"`
	NetworkPolicyName  string                `json:"networkPolicyName,omitempty"`
	Delivery           string                `json:"delivery,omitempty"`
	PRURL              string                `json:"prUrl,omitempty"`
	Attempts           []Attempt             `json:"attempts"`
	Warnings           []string              `json:"warnings,omitempty"`
	Cleanup            []string              `json:"cleanup,omitempty"`
	VerifiedPatch      string                `json:"verifiedPatch,omitempty"`
	FailureClass       string                `json:"failureClass,omitempty"`
	FailureSummary     string                `json:"failureSummary,omitempty"`
	// Caveats lists non-blocked production surfaces a PASS did not actually
	// exercise. It lowers stated confidence only; it never feeds the apply gate
	// (fix.Plan.ApplyEligible).
	Caveats []string `json:"caveats,omitempty"`
}

type IsolationReport struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Checks  []string `json:"checks,omitempty"`
}

type Attempt struct {
	Number       int      `json:"number"`
	CloneName    string   `json:"cloneName"`
	Phase        string   `json:"phase"`
	Ready        bool     `json:"ready"`
	Inconclusive bool     `json:"inconclusive,omitempty"`
	Restarts     int      `json:"restarts"`
	ExitReason   string   `json:"exitReason,omitempty"`
	Logs         []string `json:"logs,omitempty"`
	Events       []string `json:"events,omitempty"`
	Revised      bool     `json:"revised,omitempty"`
	Message      string   `json:"message,omitempty"`
}

type PatchValidationError struct {
	Reasons []string
}

func (e PatchValidationError) Error() string {
	if len(e.Reasons) == 0 {
		return "revised patch rejected"
	}
	return "revised patch rejected: " + strings.Join(e.Reasons, "; ")
}
