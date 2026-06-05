package shadow

import (
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

type DeliveryMode string

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
	Verified          bool      `json:"verified"`
	Parity            int       `json:"parity"`
	Resource          string    `json:"resource"`
	Namespace         string    `json:"namespace"`
	CloneName         string    `json:"cloneName,omitempty"`
	NetworkPolicyName string    `json:"networkPolicyName,omitempty"`
	Delivery          string    `json:"delivery,omitempty"`
	PRURL             string    `json:"prUrl,omitempty"`
	Attempts          []Attempt `json:"attempts"`
	Warnings          []string  `json:"warnings,omitempty"`
	Cleanup           []string  `json:"cleanup,omitempty"`
	VerifiedPatch     string    `json:"verifiedPatch,omitempty"`
}

type Attempt struct {
	Number     int      `json:"number"`
	CloneName  string   `json:"cloneName"`
	Phase      string   `json:"phase"`
	Ready      bool     `json:"ready"`
	Restarts   int      `json:"restarts"`
	ExitReason string   `json:"exitReason,omitempty"`
	Logs       []string `json:"logs,omitempty"`
	Events     []string `json:"events,omitempty"`
	Revised    bool     `json:"revised,omitempty"`
	Message    string   `json:"message,omitempty"`
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
