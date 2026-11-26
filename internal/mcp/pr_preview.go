package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/fixora/kubectl-fixora/internal/repo"
)

const (
	mcpPRPreviewTTL = 10 * time.Minute
	mcpPRPreviewMax = 16
)

type mcpPRPreviewRegistry struct {
	mu      sync.Mutex
	entries map[string]mcpPRPreviewEntry
}

type mcpPRPreviewEntry struct {
	preview repo.MCPPRPreview
	expires time.Time
}

func newMCPPRPreviewRegistry() *mcpPRPreviewRegistry {
	return &mcpPRPreviewRegistry{entries: map[string]mcpPRPreviewEntry{}}
}

func (r *mcpPRPreviewRegistry) add(preview repo.MCPPRPreview) (string, error) {
	if r == nil {
		return "", errors.New("MCP PR preview registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, entry := range r.entries {
		if !entry.expires.After(now) {
			delete(r.entries, id)
		}
	}
	if len(r.entries) >= mcpPRPreviewMax {
		return "", errors.New("too many pending PR previews; wait for one to expire or restart the MCP server")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("could not create a private PR preview handle")
	}
	id := hex.EncodeToString(raw)
	r.entries[id] = mcpPRPreviewEntry{preview: preview, expires: now.Add(mcpPRPreviewTTL)}
	return id, nil
}

func (r *mcpPRPreviewRegistry) take(id string) (repo.MCPPRPreview, bool) {
	if r == nil || id == "" {
		return repo.MCPPRPreview{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return repo.MCPPRPreview{}, false
	}
	delete(r.entries, id)
	if !entry.expires.After(time.Now()) {
		return repo.MCPPRPreview{}, false
	}
	return entry.preview, true
}
