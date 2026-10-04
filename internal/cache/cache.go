package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

type Store struct {
	Dir string
}

type Stats struct {
	Dir     string `json:"dir"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
}

func New() Store {
	dir := strings.TrimSpace(os.Getenv("FIXORA_CACHE_DIR"))
	if dir == "" {
		if userCache, err := os.UserCacheDir(); err == nil {
			dir = filepath.Join(userCache, "fixora", "cli")
		}
	}
	return Store{Dir: dir}
}

func Key(f analyzer.Finding) string {
	payload := f.ID + "|" + f.Summary + "|" + f.Status + "|" + f.Category
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// AIKey separates explanations by their non-secret request identity. API keys
// are intentionally never accepted as an input to cache-key construction.
func AIKey(f analyzer.Finding, provider, protocol, model, endpoint, profile, outputMode string) string {
	identity := struct {
		Finding    analyzer.Finding `json:"finding"`
		Provider   string           `json:"provider"`
		Protocol   string           `json:"protocol"`
		Model      string           `json:"model"`
		Endpoint   string           `json:"endpoint"`
		Profile    string           `json:"profile"`
		OutputMode string           `json:"outputMode"`
	}{f, provider, protocol, model, endpointHost(endpoint), profile, outputMode}
	payload, _ := json.Marshal(identity)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func endpointHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (s Store) Get(key string, target any) bool {
	if s.Dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, key+".json"))
	if err != nil {
		return false
	}
	return json.Unmarshal(data, target) == nil
}

func (s Store) Set(key string, value any) error {
	if s.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, key+".json"), data, 0o600)
}

func (s Store) Clear() error {
	if s.Dir == "" {
		return nil
	}
	return os.RemoveAll(s.Dir)
}

func (s Store) Stats() Stats {
	stats := Stats{Dir: s.Dir}
	if s.Dir == "" {
		return stats
	}
	_ = filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".json") {
			stats.Entries++
			if info, infoErr := d.Info(); infoErr == nil {
				stats.Bytes += info.Size()
			}
		}
		return nil
	})
	return stats
}
