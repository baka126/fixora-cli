package workloadmemory

import (
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
	schemaVersion          = 1
	maxSessionsPerWorkload = 10
	maxProfilesPerCluster  = 200
	maxProfileBytes        = 256 * 1024
	maxHistoryAge          = 90 * 24 * time.Hour
)

const (
	OutcomeVerified     = "verified"
	OutcomeFailed       = "failed"
	OutcomeInconclusive = "inconclusive"
	OutcomeBlocked      = "blocked"
)

var (
	clusterIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	storeMu          sync.Mutex
)

type Store struct {
	Path string
}

type ProfileKey struct {
	ClusterID       string
	Namespace       string
	ResourceKind    string
	ResourceName    string
	ImageRepository string
}

type Session struct {
	At                     time.Time `json:"at"`
	Outcome                string    `json:"outcome"`
	FailureClass           string    `json:"failureClass,omitempty"`
	Strategy               string    `json:"strategy,omitempty"`
	Parity                 int       `json:"parity"`
	TimeToReadySeconds     int64     `json:"timeToReadySeconds,omitempty"`
	MemoryPeakBytes        int64     `json:"memoryPeakBytes,omitempty"`
	MemoryLimitBytes       int64     `json:"memoryLimitBytes,omitempty"`
	MemoryHeadroomPercent  int       `json:"memoryHeadroomPercent,omitempty"`
	RepairMemoryLimitBytes int64     `json:"repairMemoryLimitBytes,omitempty"`
}

type Profile struct {
	Sessions []Session `json:"sessions"`
	LastSeen time.Time `json:"lastSeen"`
}

type diskStore struct {
	SchemaVersion int                `json:"schemaVersion"`
	Profiles      map[string]Profile `json:"profiles"`
}

func Fingerprint(key ProfileKey) string {
	clusterID := strings.ToLower(strings.TrimSpace(key.ClusterID))
	namespace := strings.ToLower(strings.TrimSpace(key.Namespace))
	kind := strings.ToLower(strings.TrimSpace(key.ResourceKind))
	name := strings.ToLower(strings.TrimSpace(key.ResourceName))
	repository := normalizeImageRepository(key.ImageRepository)
	if !clusterIDPattern.MatchString(clusterID) || namespace == "" || kind == "" || name == "" || repository == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{clusterID, namespace, kind, name, repository}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func normalizeImageRepository(reference string) string {
	reference = strings.ToLower(strings.TrimSpace(reference))
	if reference == "" || strings.ContainsAny(reference, " \t\r\n") {
		return ""
	}
	if at := strings.IndexByte(reference, '@'); at >= 0 {
		reference = reference[:at]
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	if colon := strings.LastIndexByte(reference, ':'); colon > lastSlash {
		reference = reference[:colon]
	}
	return strings.TrimSuffix(reference, "/")
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
	return filepath.Join(home, ".fixora", clusterID, "profiles.json"), nil
}

func (p Profile) LatestVerified() (Session, bool) {
	for i := len(p.Sessions) - 1; i >= 0; i-- {
		if p.Sessions[i].Outcome == OutcomeVerified {
			return p.Sessions[i], true
		}
	}
	return Session{}, false
}

func (s Store) Load(fingerprint string) (Profile, bool, error) {
	if !clusterIDPattern.MatchString(strings.ToLower(fingerprint)) {
		return Profile{}, false, errors.New("invalid workload fingerprint")
	}
	state, err := s.read()
	if err != nil {
		return Profile{}, false, err
	}
	profile, ok := state.Profiles[fingerprint]
	if !ok {
		return Profile{}, false, nil
	}
	cutoff := time.Now().UTC().Add(-maxHistoryAge)
	sessions := profile.Sessions[:0]
	for _, session := range profile.Sessions {
		if session.At.After(cutoff) {
			sessions = append(sessions, session)
		}
	}
	profile.Sessions = sessions
	if len(profile.Sessions) == 0 {
		return Profile{}, false, nil
	}
	return profile, true, nil
}

func (s Store) Record(fingerprint string, session Session) error {
	if !clusterIDPattern.MatchString(strings.ToLower(fingerprint)) {
		return errors.New("invalid workload fingerprint")
	}
	session, err := normalizeSession(session)
	if err != nil {
		return err
	}
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("workload-memory path is required")
	}

	storeMu.Lock()
	defer storeMu.Unlock()

	state, err := s.read()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Profiles == nil {
		state = diskStore{SchemaVersion: schemaVersion, Profiles: map[string]Profile{}}
	}
	cutoff := time.Now().UTC().Add(-maxHistoryAge)
	profile := state.Profiles[fingerprint]
	sessions := make([]Session, 0, len(profile.Sessions)+1)
	for _, prior := range profile.Sessions {
		if prior.At.After(cutoff) {
			sessions = append(sessions, prior)
		}
	}
	sessions = append(sessions, session)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].At.Before(sessions[j].At) })
	if len(sessions) > maxSessionsPerWorkload {
		sessions = append([]Session(nil), sessions[len(sessions)-maxSessionsPerWorkload:]...)
	}
	state.Profiles[fingerprint] = Profile{Sessions: sessions, LastSeen: session.At}
	pruneProfiles(&state, fingerprint)
	return s.write(state)
}

func normalizeSession(session Session) (Session, error) {
	switch session.Outcome {
	case OutcomeVerified, OutcomeFailed, OutcomeInconclusive, OutcomeBlocked:
	default:
		return Session{}, errors.New("invalid workload-memory outcome")
	}
	session.FailureClass = safeToken(session.FailureClass)
	session.Strategy = safeToken(session.Strategy)
	if session.Parity < 0 || session.Parity > 100 || session.TimeToReadySeconds < 0 ||
		session.MemoryPeakBytes < 0 || session.MemoryLimitBytes < 0 ||
		session.MemoryHeadroomPercent < 0 || session.MemoryHeadroomPercent > 100 ||
		session.RepairMemoryLimitBytes < 0 {
		return Session{}, errors.New("invalid workload-memory measurements")
	}
	if session.At.IsZero() {
		session.At = time.Now().UTC()
	} else {
		session.At = session.At.UTC()
	}
	return session, nil
}

func safeToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 80 {
		value = value[:80]
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return ""
		}
	}
	return value
}

func pruneProfiles(state *diskStore, current string) {
	cutoff := time.Now().UTC().Add(-maxHistoryAge)
	for key, profile := range state.Profiles {
		sessions := profile.Sessions[:0]
		for _, session := range profile.Sessions {
			if session.At.After(cutoff) {
				sessions = append(sessions, session)
			}
		}
		if len(sessions) == 0 {
			if key != current {
				delete(state.Profiles, key)
			}
			continue
		}
		profile.Sessions = sessions
		state.Profiles[key] = profile
	}
	for len(state.Profiles) > maxProfilesPerCluster {
		oldestKey := ""
		var oldest time.Time
		for key, profile := range state.Profiles {
			if key == current {
				continue
			}
			if oldestKey == "" || profile.LastSeen.Before(oldest) {
				oldestKey, oldest = key, profile.LastSeen
			}
		}
		if oldestKey == "" {
			break
		}
		delete(state.Profiles, oldestKey)
	}
}

func (s Store) read() (diskStore, error) {
	if strings.TrimSpace(s.Path) == "" {
		return diskStore{}, errors.New("workload-memory path is required")
	}
	file, err := os.Open(s.Path)
	if err != nil {
		return diskStore{SchemaVersion: schemaVersion, Profiles: map[string]Profile{}}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxProfileBytes+1))
	if err != nil {
		return diskStore{}, err
	}
	if len(data) > maxProfileBytes {
		return diskStore{}, errors.New("workload-memory profile exceeds size limit")
	}
	var state diskStore
	if err := json.Unmarshal(data, &state); err != nil {
		return diskStore{}, fmt.Errorf("invalid workload-memory profile: %w", err)
	}
	if state.SchemaVersion != schemaVersion {
		return diskStore{}, errors.New("unsupported workload-memory profile version")
	}
	if state.Profiles == nil {
		state.Profiles = map[string]Profile{}
	}
	for key := range state.Profiles {
		if !clusterIDPattern.MatchString(key) {
			return diskStore{}, errors.New("invalid workload-memory profile key")
		}
	}
	return state, nil
}

func (s Store) write(state diskStore) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for {
		data, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		if len(data) <= maxProfileBytes {
			return writeAtomic(s.Path, dir, data)
		}
		if len(state.Profiles) <= 1 {
			return errors.New("workload-memory profile exceeds size limit")
		}
		oldestKey := ""
		var oldest time.Time
		for key, profile := range state.Profiles {
			if oldestKey == "" || profile.LastSeen.Before(oldest) {
				oldestKey, oldest = key, profile.LastSeen
			}
		}
		delete(state.Profiles, oldestKey)
	}
}

func writeAtomic(path, dir string, data []byte) error {
	file, err := os.CreateTemp(dir, ".profiles.tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
