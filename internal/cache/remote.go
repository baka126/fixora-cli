package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RemoteConfig struct {
	Type           string `json:"type"`
	Bucket         string `json:"bucket,omitempty"`
	Region         string `json:"region,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	Insecure       bool   `json:"insecure,omitempty"`
	StorageAccount string `json:"storageAccount,omitempty"`
	Container      string `json:"container,omitempty"`
	ProjectID      string `json:"projectID,omitempty"`
}

type Entry struct {
	Key   string `json:"key"`
	Size  int64  `json:"size"`
	Local bool   `json:"local"`
}

func (s Store) RemoteConfigPath() string {
	return filepath.Join(s.Dir, "remote-cache.json")
}

func (s Store) SetRemote(cfg RemoteConfig) error {
	fmt.Fprintln(os.Stderr, "warning: remote caching is currently a stub and not fully implemented")
	cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))
	switch cfg.Type {
	case "s3":
		if cfg.Bucket == "" {
			return fmt.Errorf("s3 cache requires bucket")
		}
	case "azure":
		if cfg.StorageAccount == "" || cfg.Container == "" {
			return fmt.Errorf("azure cache requires storage account and container")
		}
	case "gcs":
		if cfg.Bucket == "" || cfg.ProjectID == "" {
			return fmt.Errorf("gcs cache requires bucket and project id")
		}
	case "interplex":
		if cfg.Endpoint == "" {
			return fmt.Errorf("interplex cache requires endpoint")
		}
	default:
		return fmt.Errorf("cache type must be one of s3, azure, gcs, interplex")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.RemoteConfigPath(), data, 0o600)
}

func (s Store) Remote() (RemoteConfig, error) {
	var cfg RemoteConfig
	data, err := os.ReadFile(s.RemoteConfigPath())
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}

func (s Store) RemoveRemote() error {
	err := os.Remove(s.RemoteConfigPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s Store) List() []Entry {
	entries := []Entry{}
	if s.Dir == "" {
		return entries
	}
	_ = filepath.WalkDir(s.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || d.Name() == "remote-cache.json" {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		entries = append(entries, Entry{Key: strings.TrimSuffix(d.Name(), ".json"), Size: info.Size(), Local: true})
		return nil
	})
	return entries
}

func (s Store) Purge(key string) error {
	key = strings.TrimSuffix(filepath.Base(strings.TrimSpace(key)), ".json")
	if key == "" || key == "." {
		return fmt.Errorf("cache purge requires key")
	}
	err := os.Remove(filepath.Join(s.Dir, key+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
