package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
)

const (
	maxRecords = 1000
	recordTTL  = 30 * 24 * time.Hour
)

type Record struct {
	Time      time.Time `json:"time"`
	Key       string    `json:"key"`
	Resource  string    `json:"resource"`
	Status    string    `json:"status"`
	Summary   string    `json:"summary"`
	Strategy  string    `json:"strategy"`
	Outcome   string    `json:"outcome,omitempty"`
	Namespace string    `json:"namespace,omitempty"`
}

func lock() (func(), error) {
	lockFile := path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockFile), 0o700); err != nil {
		return func() {}, err
	}
	for i := 0; i < 50; i++ {
		f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(lockFile) }, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {}, fmt.Errorf("timeout acquiring memory file lock")
}

func Add(f analyzer.Finding, p fix.Plan, outcome string) error {
	unlock, err := lock()
	if err != nil {
		return err
	}
	defer unlock()

	records, _ := listUnlocked()
	cutoff := time.Now().Add(-recordTTL)
	valid := make([]Record, 0, len(records)+1)
	
	for _, r := range records {
		if r.Time.After(cutoff) {
			valid = append(valid, r)
		}
	}
	
	valid = append(valid, Record{
		Time:      time.Now(),
		Key:       Key(f),
		Resource:  f.ResourceKind + "/" + f.ResourceName,
		Status:    f.Status,
		Summary:   f.Summary,
		Strategy:  p.Strategy,
		Outcome:   outcome,
		Namespace: f.Namespace,
	})
	
	if len(valid) > maxRecords {
		valid = valid[len(valid)-maxRecords:]
	}
	
	return saveUnlocked(valid)
}

func List() ([]Record, error) {
	unlock, err := lock()
	if err != nil {
		return listUnlocked()
	}
	defer unlock()
	return listUnlocked()
}

func listUnlocked() ([]Record, error) {
	data, err := os.ReadFile(path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	return records, json.Unmarshal(data, &records)
}

func Clear() error {
	unlock, err := lock()
	if err != nil {
		return os.Remove(path())
	}
	defer unlock()
	return os.Remove(path())
}

func Match(f analyzer.Finding) []Record {
	records, _ := List()
	out := []Record{}
	key := Key(f)
	for _, record := range records {
		if record.Key == key {
			out = append(out, record)
		}
	}
	return out
}

func Key(f analyzer.Finding) string {
	return f.ResourceKind + "|" + f.Status + "|" + f.Category + "|" + f.Summary
}

func saveUnlocked(records []Record) error {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func path() string {
	if p := os.Getenv("FIXORA_MEMORY_FILE"); p != "" {
		return p
	}
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "fixora", "cli", "memory.json")
}
