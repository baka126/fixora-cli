package asksession

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreCreateLoadListAndDelete(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	cluster := strings.Repeat("a", 64)
	created, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Summary: "pod is restarting", Turns: []Turn{{Request: "why is it restarting?", AnswerSummary: "container exits", At: time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 12 {
		t.Fatalf("generated ID = %q, want 12 hex characters", created.ID)
	}
	loaded, err := store.Load(created.ID, cluster)
	if err != nil || loaded.ResourceName != "api" || len(loaded.Turns) != 1 {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	listed, err := store.List(cluster)
	if err != nil || len(listed) != 1 || len(listed[0].Turns) != 0 {
		t.Fatalf("List() = %#v, %v; listing must omit turn contents", listed, err)
	}
	if err := store.Delete(created.ID, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(created.ID, cluster); err == nil {
		t.Fatal("Load() after Delete() succeeded")
	}
}

func TestStoreRejectsClusterMismatch(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	created, err := store.Create(Session{ClusterID: strings.Repeat("a", 64), Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(created.ID, strings.Repeat("b", 64)); err == nil {
		t.Fatal("Load() accepted a session from another cluster")
	}
}

func TestStoreExpiresInactiveSession(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json"), Now: func() time.Time { return now }}
	cluster := strings.Repeat("a", 64)
	created, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30*24*time.Hour + time.Second)
	if _, err := store.Load(created.ID, cluster); err == nil {
		t.Fatal("Load() accepted an inactive session older than 30 days")
	}
}

func TestStoreCapsSessionsWithoutEvicting(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	cluster := strings.Repeat("a", 64)
	firstID := ""
	for i := 0; i < 50; i++ {
		created, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"})
		if err != nil {
			t.Fatalf("Create() %d: %v", i, err)
		}
		if i == 0 {
			firstID = created.ID
		}
	}
	if _, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "worker"}); err == nil {
		t.Fatal("Create() silently exceeded the 50-session cap")
	}
	if _, err := store.Load(firstID, cluster); err != nil {
		t.Fatalf("session was evicted at capacity: %v", err)
	}
}

func TestStoreRejectsTurnAndTextOverflow(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	cluster := strings.Repeat("a", 64)
	tooManyTurns := make([]Turn, 21)
	if _, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Turns: tooManyTurns}); err == nil {
		t.Fatal("Create() accepted more than 20 turns")
	}
	if _, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Summary: strings.Repeat("x", 24<<10)}); err != nil {
		t.Fatalf("Create() rejected the exact 24 KiB limit: %v", err)
	}
	if _, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "worker", Summary: strings.Repeat("x", (24<<10)+1)}); err == nil {
		t.Fatal("Create() accepted more than 24 KiB of conversational text")
	}
}

func TestStoreRedactsStructuredSecretsBeforePersistence(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	cluster := strings.Repeat("a", 64)
	created, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", Turns: []Turn{{Request: `{"token":"request-secret"}`, AnswerSummary: "Authorization: Bearer response-secret", At: time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(created.ID, cluster)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"request-secret", "response-secret"} {
		if strings.Contains(string(serialized), secret) {
			t.Errorf("session store persisted secret %q", secret)
		}
	}
	if !strings.Contains(loaded.Turns[0].Request, "[REDACTED]") || !strings.Contains(loaded.Turns[0].AnswerSummary, "[REDACTED]") {
		t.Fatalf("stored conversation was not clearly redacted: %#v", loaded.Turns[0])
	}
}

func TestStoreRejectsCorruptAndUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	for _, data := range []string{"not-json", `{"schemaVersion":999,"sessions":[]}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Path: path}).List(strings.Repeat("a", 64)); err == nil {
			t.Fatalf("List() accepted corrupt or unsupported data %q", data)
		}
	}
}

func TestStorePreservesExistingDataOnAtomicWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(path, "keep")
	if err := os.WriteFile(keep, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: path}
	if err := store.Save(Session{ID: "0123456789ab", ClusterID: strings.Repeat("a", 64), Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"}); err == nil {
		t.Fatal("Save() unexpectedly replaced a directory")
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "preserve" {
		t.Fatalf("existing data changed after failed write: %q, %v", got, err)
	}
}

func TestStoreUsesPrivateDirectoryAndFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "sessions.json")
	store := Store{Path: path}
	if _, err := store.Create(Session{ClusterID: strings.Repeat("a", 64), Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api"}); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %#o, want 0700", got)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", got)
	}
}

func TestStorePreservesOwnerChainOnRoundTrip(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "sessions.json")}
	cluster := strings.Repeat("a", 64)
	created, err := store.Create(Session{ClusterID: cluster, Namespace: "payments", ResourceKind: "Deployment", ResourceName: "api", OwnerChain: []string{"ReplicaSet/api-abc", "Deployment/api"}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(created.ID, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.OwnerChain, []string{"ReplicaSet/api-abc", "Deployment/api"}) {
		t.Fatalf("OwnerChain = %#v", loaded.OwnerChain)
	}
}
