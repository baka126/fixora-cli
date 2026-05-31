package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
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

func Add(f analyzer.Finding, p fix.Plan, outcome string) error {
	records, _ := List()
	records = append(records, Record{
		Time:      time.Now(),
		Key:       Key(f),
		Resource:  f.ResourceKind + "/" + f.ResourceName,
		Status:    f.Status,
		Summary:   f.Summary,
		Strategy:  p.Strategy,
		Outcome:   outcome,
		Namespace: f.Namespace,
	})
	return save(records)
}

func List() ([]Record, error) {
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

func save(records []Record) error {
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path(), data, 0o600)
}

func path() string {
	if p := os.Getenv("FIXORA_MEMORY_FILE"); p != "" {
		return p
	}
	dir, _ := os.UserCacheDir()
	return filepath.Join(dir, "fixora", "cli", "memory.json")
}
