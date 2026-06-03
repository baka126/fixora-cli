package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
