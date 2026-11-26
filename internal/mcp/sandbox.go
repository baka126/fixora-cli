package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

const (
	maxOpenSandboxes = 3
	sandboxTTL       = 30 * time.Minute
	sandboxRetryTTL  = 5 * time.Minute
)

type sandboxRegistry struct {
	mu      sync.Mutex
	entries map[string]sandboxEntry
	pending map[string]struct{}
	client  func() (*kube.TypedClient, error)
}

type sandboxEntry struct {
	candidate shadow.CleanupCandidate
	expiresAt time.Time
	timer     *time.Timer
}

func newSandboxRegistry(client func() (*kube.TypedClient, error)) *sandboxRegistry {
	return &sandboxRegistry{entries: map[string]sandboxEntry{}, pending: map[string]struct{}{}, client: client}
}

func (r *sandboxRegistry) reserve() (string, error) {
	if r == nil {
		return "", errors.New("verified sandbox registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries)+len(r.pending) >= maxOpenSandboxes {
		return "", errors.New("Fixora MCP already has the maximum of three open sandboxes; close one before opening another")
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("could not create a private sandbox handle")
	}
	id := hex.EncodeToString(raw)
	r.pending[id] = struct{}{}
	return id, nil
}

func (r *sandboxRegistry) cancelReservation(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

func (r *sandboxRegistry) commit(id string, candidate shadow.CleanupCandidate) (time.Time, error) {
	if r == nil {
		return time.Time{}, errors.New("verified sandbox registry is unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[id]; !ok {
		return time.Time{}, errors.New("private sandbox reservation expired")
	}
	delete(r.pending, id)
	expiresAt := time.Now().Add(sandboxTTL)
	entry := sandboxEntry{candidate: candidate, expiresAt: expiresAt}
	entry.timer = time.AfterFunc(sandboxTTL, func() { r.expire(id) })
	r.entries[id] = entry
	return expiresAt, nil
}

func (r *sandboxRegistry) close(ctx context.Context, id string) error {
	if r == nil {
		return errors.New("verified sandbox registry is unavailable")
	}
	r.mu.Lock()
	entry, ok := r.entries[id]
	r.mu.Unlock()
	if !ok {
		return errors.New("sandbox handle is unknown, expired, or already closed")
	}
	if r.client == nil {
		r.retryLater(id)
		return errors.New("Kubernetes client is unavailable for sandbox cleanup; retry or use kubectl fixora cleanup")
	}
	client, err := r.client()
	if err != nil {
		r.retryLater(id)
		return errors.New("could not connect to Kubernetes for sandbox cleanup; retry or use kubectl fixora cleanup")
	}
	if err := shadow.DeleteCleanupCandidate(ctx, client, entry.candidate); err != nil {
		r.retryLater(id)
		return errors.New("sandbox cleanup could not be confirmed; retry close-sandbox or use kubectl fixora cleanup")
	}
	r.mu.Lock()
	if current, exists := r.entries[id]; exists && current.candidate.UID == entry.candidate.UID {
		if current.timer != nil {
			current.timer.Stop()
		}
		delete(r.entries, id)
	}
	r.mu.Unlock()
	return nil
}

func (r *sandboxRegistry) expire(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_ = r.close(ctx, id)
}

func (r *sandboxRegistry) retryLater(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	entry.timer = time.AfterFunc(sandboxRetryTTL, func() { r.expire(id) })
	r.entries[id] = entry
}

func (r *sandboxRegistry) closeAll(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ids := make([]string, 0, len(r.entries))
	for id := range r.entries {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	failed := false
	for _, id := range ids {
		cleanupCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := r.close(cleanupCtx, id)
		cancel()
		if err != nil {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more verified MCP sandboxes remain; run kubectl fixora cleanup")
	}
	return nil
}
