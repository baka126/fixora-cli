package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/redact"
	"github.com/fixora/kubectl-fixora/internal/repo"
)

const mcpPRAttestation = "Operator attestation: the existing branch was verified by the operator. Fixora did not match this branch's source diff to a prior shadow verification."

type createPRInput struct {
	Base                string `json:"base" jsonschema:"Required base branch name"`
	PreviewID           string `json:"previewId,omitempty" jsonschema:"For confirmation, copy the short-lived handle returned by the preview"`
	Title               string `json:"title,omitempty" jsonschema:"Optional pull or merge request title"`
	Description         string `json:"description,omitempty" jsonschema:"Optional additional description for the review request"`
	VerificationSummary string `json:"verificationSummary,omitempty" jsonschema:"Operator-supplied summary of how the existing branch was verified"`
	ExpectedBranch      string `json:"expectedBranch,omitempty" jsonschema:"For confirmation, copy the branch name returned by the preview"`
	ExpectedHeadCommit  string `json:"expectedHeadCommit,omitempty" jsonschema:"For confirmation, copy the HEAD commit returned by the preview"`
	ExpectedBaseCommit  string `json:"expectedBaseCommit,omitempty" jsonschema:"For confirmation, copy the base commit returned by the preview"`
	Confirm             bool   `json:"confirm,omitempty" jsonschema:"False previews only; true confirms pushing the prepared branch and opening a review request"`
}

type createPRSummary struct {
	Preview              *repo.MCPPRPreview `json:"preview,omitempty"`
	PreviewID            string             `json:"previewId,omitempty"`
	VerificationNote     string             `json:"verificationNote"`
	VerificationSummary  string             `json:"verificationSummary,omitempty"`
	ConfirmationRequired bool               `json:"confirmationRequired,omitempty"`
	Pushed               bool               `json:"pushed"`
	Opened               bool               `json:"opened"`
	URL                  string             `json:"url,omitempty"`
	Platform             string             `json:"platform,omitempty"`
	Warnings             []string           `json:"warnings,omitempty"`
	Message              string             `json:"message,omitempty"`
	FailureSummary       string             `json:"failureSummary,omitempty"`
}

func (s Server) callCreatePR(ctx context.Context, input createPRInput) (createPRSummary, error) {
	if strings.TrimSpace(s.PullRequestRepo) == "" {
		return createPRSummary{}, errors.New("MCP create-pr is disabled; configure --mcp-pr-repo at server startup")
	}
	input.Title = redact.Text(input.Title)
	if s.prPreviews == nil {
		return createPRSummary{}, errors.New("MCP PR preview registry is unavailable; restart the MCP server")
	}
	if !input.Confirm {
		preview, err := repo.PreviewMCPPR(ctx, s.PullRequestRepo, input.Base, input.Title)
		if err != nil {
			return createPRSummary{}, err
		}
		previewID, err := s.prPreviews.add(preview)
		if err != nil {
			return createPRSummary{}, err
		}
		summary := createPRSummary{
			Preview:             &preview,
			PreviewID:           previewID,
			VerificationNote:    mcpPRAttestation,
			VerificationSummary: strings.TrimSpace(redact.Text(input.VerificationSummary)),
		}
		summary.ConfirmationRequired = true
		summary.Message = "Review this branch, base commit, and verification summary. To publish it within 10 minutes, confirm and echo previewId, expectedBranch, expectedHeadCommit, and expectedBaseCommit from this preview."
		return summary, nil
	}
	if strings.TrimSpace(input.PreviewID) == "" || strings.TrimSpace(input.ExpectedBranch) == "" || strings.TrimSpace(input.ExpectedHeadCommit) == "" || strings.TrimSpace(input.ExpectedBaseCommit) == "" {
		return createPRSummary{}, errors.New("confirmed create-pr requires previewId, expectedBranch, expectedHeadCommit, and expectedBaseCommit from the reviewed preview")
	}
	preview, ok := s.prPreviews.take(strings.TrimSpace(input.PreviewID))
	if !ok {
		return createPRSummary{}, errors.New("PR preview expired or was already used; preview the branch again")
	}
	summary := createPRSummary{Preview: &preview, VerificationNote: mcpPRAttestation, VerificationSummary: strings.TrimSpace(redact.Text(input.VerificationSummary))}
	if strings.TrimSpace(input.Base) != preview.Base || strings.TrimSpace(input.ExpectedBranch) != preview.Branch ||
		strings.TrimSpace(input.ExpectedHeadCommit) != preview.HeadCommit || strings.TrimSpace(input.ExpectedBaseCommit) != preview.BaseCommit {
		return summary, errors.New("repository, branch, or base changed since the preview; review a fresh preview before confirming")
	}
	if input.Title != "" && input.Title != preview.Title {
		return summary, errors.New("PR title changed since the preview; review a fresh preview before confirming")
	}
	body := composeMCPPRBody(input)
	result, err := repo.CreateMCPPR(ctx, s.PullRequestRepo, preview, body)
	summary.Pushed = result.Pushed
	summary.Opened = result.Opened
	summary.URL = result.URL
	summary.Platform = result.Platform
	summary.Warnings = result.Warnings
	if err != nil {
		return summary, err
	}
	summary.ConfirmationRequired = false
	if summary.Opened && summary.URL == "" {
		summary.Message = "The provider accepted the review request but returned no usable URL; inspect the repository before retrying. The cluster was not changed."
	} else {
		summary.Message = "Review request opened; the Kubernetes cluster was not changed."
	}
	return summary, nil
}

func composeMCPPRBody(input createPRInput) string {
	parts := []string{mcpPRAttestation}
	if summary := strings.TrimSpace(redact.Text(input.VerificationSummary)); summary != "" {
		parts = append(parts, "Operator-supplied verification summary:\n"+summary)
	}
	if description := strings.TrimSpace(redact.Text(input.Description)); description != "" {
		parts = append(parts, description)
	}
	return strings.Join(parts, "\n\n")
}

func safeCreatePRResult(value createPRSummary) (any, string, error) {
	copy := value
	replacements := map[string]string{}
	copy.PreviewID = protectPreviewHandle(copy.PreviewID, replacements)
	if value.Preview != nil {
		preview := *value.Preview
		preview.BaseCommit = protectGitOID(preview.BaseCommit, "BASE", replacements)
		preview.HeadCommit = protectGitOID(preview.HeadCommit, "HEAD", replacements)
		copy.Preview = &preview
	}
	safe, text, err := safeResult(copy)
	if err != nil {
		return nil, "", err
	}
	for placeholder, oid := range replacements {
		text = strings.ReplaceAll(text, placeholder, oid)
	}
	if err := json.Unmarshal([]byte(text), &safe); err != nil {
		return nil, "", err
	}
	return safe, text, nil
}

func protectPreviewHandle(handle string, replacements map[string]string) string {
	if len(handle) != 48 {
		return handle
	}
	for _, ch := range handle {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return handle
		}
	}
	placeholder := "FIXORA_PR_PREVIEW_HANDLE"
	replacements[placeholder] = handle
	return placeholder
}

func protectGitOID(oid, name string, replacements map[string]string) string {
	if !isGitOID(oid) {
		return oid
	}
	placeholder := "FIXORA_GIT_OID_" + name
	replacements[placeholder] = oid
	return placeholder
}

func isGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}
