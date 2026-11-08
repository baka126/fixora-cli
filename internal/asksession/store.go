// Package asksession stores bounded, private, local Ask conversations.
package asksession

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	schemaVersion   = 1
	maxSessions     = 50
	maxTurns        = 20
	maxTextBytes    = 24 << 10
	sessionMaxAge   = 30 * 24 * time.Hour
	maxStoreBytes   = 2 << 20
	maxRequestBytes = 8 << 10
	maxSummaryBytes = 8 << 10
)

var (
	clusterIDPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	sessionIDPattern  = regexp.MustCompile(`^[a-f0-9]{12}$`)
	secretPattern     = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|token|password|passwd|secret)(\s*[:=]\s*)([^\s,;]+)`)
	jsonSecretPattern = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|token|password|passwd|secret)["']?\s*:\s*["'])([^"']*)(["'])`)
	bearerPattern     = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	storeMu           sync.Mutex
)

type Turn struct {
	Request       string    `json:"request"`
	AnswerSummary string    `json:"answerSummary"`
	FollowUp      string    `json:"followUp,omitempty"`
	At            time.Time `json:"at"`
}

// EvidenceRef persists provenance only, never the observed evidence value.
type EvidenceRef struct {
	ID       string    `json:"id"`
	Resource string    `json:"resource"`
	Signal   string    `json:"signal"`
	At       time.Time `json:"at"`
}

type Session struct {
	ID              string        `json:"id"`
	ClusterID       string        `json:"clusterId"`
	Namespace       string        `json:"namespace"`
	ResourceKind    string        `json:"resourceKind"`
	ResourceName    string        `json:"resourceName"`
	ResourceUID     string        `json:"resourceUid,omitempty"`
	OwnerChain      []string      `json:"ownerChain,omitempty"`
	FindingID       string        `json:"findingId"`
	Status          string        `json:"status"`
	AnalyzerFilters []string      `json:"analyzerFilters,omitempty"`
	EvidenceRefs    []EvidenceRef `json:"evidenceRefs,omitempty"`
	Summary         string        `json:"summary,omitempty"`
	OpenQuestion    string        `json:"openQuestion,omitempty"`
	Turns           []Turn        `json:"turns,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

type diskStore struct {
	SchemaVersion int                `json:"schemaVersion"`
	Sessions      map[string]Session `json:"sessions"`
}

type Store struct {
	Path string
	Now  func() time.Time
}

func DefaultPath(clusterID string) (string, error) {
	clusterID = strings.ToLower(strings.TrimSpace(clusterID))
	if !clusterIDPattern.MatchString(clusterID) {
		return "", errors.New("invalid cluster identifier")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("user home directory is unavailable")
	}
	return filepath.Join(home, ".fixora", clusterID, "ask-sessions.json"), nil
}

func (s Store) Create(session Session) (Session, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if s.Path == "" {
		return Session{}, errors.New("ask session path is required")
	}
	now := s.now()
	session.ClusterID = normalizeClusterID(session.ClusterID)
	if !clusterIDPattern.MatchString(session.ClusterID) {
		return Session{}, errors.New("invalid cluster identifier")
	}
	if session.ID == "" {
		id, err := newID()
		if err != nil {
			return Session{}, fmt.Errorf("create session ID: %w", err)
		}
		session.ID = id
	}
	if !sessionIDPattern.MatchString(session.ID) {
		return Session{}, errors.New("invalid ask session ID")
	}
	if err := normalizeSession(&session, now); err != nil {
		return Session{}, err
	}
	state, err := s.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Session{}, err
	}
	state = normalizeStore(state)
	pruneExpired(&state, now)
	if _, exists := state.Sessions[session.ID]; exists {
		return Session{}, errors.New("ask session ID already exists")
	}
	if countCluster(state, session.ClusterID) >= maxSessions {
		return Session{}, errors.New("ask session limit reached for this cluster; delete an old session before starting another")
	}
	state.Sessions[session.ID] = session
	if err := s.write(state); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s Store) Load(id, clusterID string) (Session, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	id = strings.ToLower(strings.TrimSpace(id))
	clusterID = normalizeClusterID(clusterID)
	if !sessionIDPattern.MatchString(id) || !clusterIDPattern.MatchString(clusterID) {
		return Session{}, errors.New("invalid ask session ID or cluster identifier")
	}
	state, err := s.read()
	if err != nil {
		return Session{}, err
	}
	session, ok := state.Sessions[id]
	if !ok {
		return Session{}, errors.New("ask session was not found or has expired")
	}
	if session.ClusterID != clusterID {
		return Session{}, errors.New("ask session belongs to a different cluster")
	}
	if s.now().Sub(session.UpdatedAt) > sessionMaxAge {
		return Session{}, errors.New("ask session was not found or has expired")
	}
	return session, nil
}

func (s Store) Save(session Session) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	if !sessionIDPattern.MatchString(strings.TrimSpace(session.ID)) || !clusterIDPattern.MatchString(normalizeClusterID(session.ClusterID)) {
		return errors.New("invalid ask session ID or cluster identifier")
	}
	state, err := s.read()
	if err != nil {
		return err
	}
	prior, ok := state.Sessions[session.ID]
	if !ok {
		return errors.New("ask session was not found or has expired")
	}
	if prior.ClusterID != normalizeClusterID(session.ClusterID) {
		return errors.New("ask session belongs to a different cluster")
	}
	now := s.now()
	session.CreatedAt = prior.CreatedAt
	session.UpdatedAt = now
	if err := normalizeSession(&session, now); err != nil {
		return err
	}
	state.Sessions[session.ID] = session
	return s.write(state)
}

// List returns metadata only. Conversation text is deliberately omitted.
func (s Store) List(clusterID string) ([]Session, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	clusterID = normalizeClusterID(clusterID)
	if !clusterIDPattern.MatchString(clusterID) {
		return nil, errors.New("invalid cluster identifier")
	}
	state, err := s.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := s.now()
	out := make([]Session, 0)
	for _, session := range state.Sessions {
		if session.ClusterID != clusterID || now.Sub(session.UpdatedAt) > sessionMaxAge {
			continue
		}
		session.Turns = nil
		session.Summary = ""
		session.OpenQuestion = ""
		session.EvidenceRefs = nil
		session.AnalyzerFilters = nil
		session.OwnerChain = nil
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s Store) Delete(id, clusterID string) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	id = strings.ToLower(strings.TrimSpace(id))
	clusterID = normalizeClusterID(clusterID)
	if !sessionIDPattern.MatchString(id) || !clusterIDPattern.MatchString(clusterID) {
		return errors.New("invalid ask session ID or cluster identifier")
	}
	state, err := s.read()
	if err != nil {
		return err
	}
	session, ok := state.Sessions[id]
	if !ok {
		return errors.New("ask session was not found")
	}
	if session.ClusterID != clusterID {
		return errors.New("ask session belongs to a different cluster")
	}
	delete(state.Sessions, id)
	return s.write(state)
}

func (s Store) read() (diskStore, error) {
	file, err := os.Open(s.Path)
	if err != nil {
		return diskStore{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return diskStore{}, err
	}
	if !info.Mode().IsRegular() {
		return diskStore{}, errors.New("ask session store is not a regular file")
	}
	if info.Size() > maxStoreBytes {
		return diskStore{}, errors.New("ask session store exceeds size limit")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(s.Path, 0o600); err != nil {
			return diskStore{}, errors.New("ask session store permissions are too broad")
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStoreBytes+1))
	if err != nil {
		return diskStore{}, err
	}
	if len(data) > maxStoreBytes {
		return diskStore{}, errors.New("ask session store exceeds size limit")
	}
	var state diskStore
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return diskStore{}, fmt.Errorf("read ask session store: %w", err)
	}
	if state.SchemaVersion != schemaVersion || state.Sessions == nil {
		return diskStore{}, errors.New("ask session store has an unsupported schema")
	}
	return state, nil
}

func (s Store) write(state diskStore) error {
	if s.Path == "" {
		return errors.New("ask session path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create private ask session directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("secure ask session directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ask session store: %w", err)
	}
	if len(data) > maxStoreBytes {
		return errors.New("ask session store exceeds size limit")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".ask-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary ask session store: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure temporary ask session store: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write ask session store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync ask session store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ask session store: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("replace ask session store: %w", err)
	}
	return nil
}

func normalizeSession(session *Session, now time.Time) error {
	if strings.TrimSpace(session.Namespace) == "" || strings.TrimSpace(session.ResourceKind) == "" || strings.TrimSpace(session.ResourceName) == "" {
		return errors.New("ask session target identity is required")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	session.CreatedAt = session.CreatedAt.UTC()
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	session.UpdatedAt = session.UpdatedAt.UTC()
	session.Summary = redactText(session.Summary)
	session.OpenQuestion = redactText(session.OpenQuestion)
	if len(session.OwnerChain) > 16 {
		return errors.New("ask session owner chain exceeds its size limit")
	}
	for i := range session.OwnerChain {
		session.OwnerChain[i] = redactText(session.OwnerChain[i])
		if len(session.OwnerChain[i]) == 0 || len(session.OwnerChain[i]) > 256 {
			return errors.New("ask session owner chain contains an invalid identity")
		}
	}
	if len(session.Turns) > maxTurns {
		return errors.New("ask session turn limit reached; start a new session to continue")
	}
	for i := range session.Turns {
		turn := &session.Turns[i]
		turn.Request = redactText(turn.Request)
		turn.AnswerSummary = redactText(turn.AnswerSummary)
		turn.FollowUp = redactText(turn.FollowUp)
		if len(turn.Request) == 0 || len(turn.Request) > maxRequestBytes || len(turn.AnswerSummary) > maxSummaryBytes {
			return errors.New("ask session turn text is empty or exceeds its size limit")
		}
		if turn.At.IsZero() {
			turn.At = now
		}
		turn.At = turn.At.UTC()
	}
	if conversationBytes(*session) > maxTextBytes {
		return errors.New("ask session text limit reached; delete or summarize conversation text before continuing")
	}
	for i := range session.EvidenceRefs {
		r := &session.EvidenceRefs[i]
		r.Resource = redactText(r.Resource)
		r.Signal = redactText(r.Signal)
		if len(r.ID) > 80 || len(r.Resource) > 256 || len(r.Signal) > 256 {
			return errors.New("ask session evidence reference exceeds its size limit")
		}
		if r.At.IsZero() {
			r.At = now
		}
		r.At = r.At.UTC()
	}
	for _, filter := range session.AnalyzerFilters {
		if filter == "" || len(filter) > 64 || strings.ContainsAny(filter, " \t\r\n") {
			return errors.New("ask session contains an invalid analyzer filter")
		}
	}
	return nil
}

func conversationBytes(session Session) int {
	total := len(session.Summary) + len(session.OpenQuestion)
	for _, turn := range session.Turns {
		total += len(turn.Request) + len(turn.AnswerSummary) + len(turn.FollowUp)
	}
	return total
}

func redactText(value string) string {
	value = jsonSecretPattern.ReplaceAllString(value, "$1[REDACTED]$3")
	value = secretPattern.ReplaceAllString(value, "$1$2[REDACTED]")
	return bearerPattern.ReplaceAllString(value, "$1[REDACTED]")
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeClusterID(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizeStore(state diskStore) diskStore {
	if state.Sessions == nil {
		return diskStore{SchemaVersion: schemaVersion, Sessions: make(map[string]Session)}
	}
	return state
}

func pruneExpired(state *diskStore, now time.Time) {
	for id, session := range state.Sessions {
		if now.Sub(session.UpdatedAt) > sessionMaxAge {
			delete(state.Sessions, id)
		}
	}
}

func countCluster(state diskStore, clusterID string) int {
	count := 0
	for _, session := range state.Sessions {
		if session.ClusterID == clusterID {
			count++
		}
	}
	return count
}

func newID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// Fingerprint returns a stable non-reversible identifier for session lookup.
func Fingerprint(clusterID, namespace, kind, name string) string {
	clusterID = normalizeClusterID(clusterID)
	if !clusterIDPattern.MatchString(clusterID) {
		return ""
	}
	value := strings.Join([]string{clusterID, strings.ToLower(namespace), strings.ToLower(kind), strings.ToLower(name)}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
