package rbacfix

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
)

const receiptVersion = 1
const maxReceiptBytes = 256 << 10
const maxGrantReceipts = 100

var grantIDPattern = regexp.MustCompile(`^[a-f0-9]{12}$`)

// GrantReceipt contains only the exact authority and Kubernetes object identity
// needed to revoke one Fixora-created grant. It never stores credentials.
type GrantReceipt struct {
	SchemaVersion     int                 `json:"schemaVersion"`
	ID                string              `json:"id"`
	ClusterID         string              `json:"clusterId"`
	CreatedAt         time.Time           `json:"createdAt"`
	Namespace         string              `json:"namespace"`
	ServiceAccount    string              `json:"serviceAccount"`
	ServiceAccountUID string              `json:"serviceAccountUid,omitempty"`
	PolicyDigest      string              `json:"policyDigest"`
	Requests          []Request           `json:"requests"`
	Role              *rbacv1.Role        `json:"role"`
	RoleBinding       *rbacv1.RoleBinding `json:"roleBinding"`
}

type GrantSummary struct {
	ID             string    `json:"id"`
	Namespace      string    `json:"namespace"`
	ServiceAccount string    `json:"serviceAccount"`
	CreatedAt      time.Time `json:"createdAt"`
	Role           string    `json:"role"`
	RoleBinding    string    `json:"roleBinding"`
}

type GrantStore struct {
	ClusterID string
	Dir       string
}

func NewGrantStore(clusterID string) (*GrantStore, error) {
	clusterID = strings.ToLower(strings.TrimSpace(clusterID))
	if len(clusterID) != 64 {
		return nil, errors.New("invalid cluster identifier")
	}
	if _, err := hex.DecodeString(clusterID); err != nil {
		return nil, errors.New("invalid cluster identifier")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, errors.New("user home directory is unavailable")
	}
	return &GrantStore{ClusterID: clusterID, Dir: filepath.Join(home, ".fixora", clusterID, "rbac-grants")}, nil
}

func (s *GrantStore) Create(r GrantReceipt) (GrantReceipt, error) {
	if s == nil || s.Dir == "" {
		return r, errors.New("grant receipt store is unavailable")
	}
	if err := s.secureDir(); err != nil {
		return r, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return r, err
	}
	if len(entries) >= maxGrantReceipts {
		return r, errors.New("grant receipt limit reached; remove old receipts after confirming their grants are gone")
	}
	for i := 0; i < 8; i++ {
		var b [6]byte
		if _, err = rand.Read(b[:]); err != nil {
			return r, err
		}
		r.ID = hex.EncodeToString(b[:])
		r.SchemaVersion = receiptVersion
		r.ClusterID = s.ClusterID
		if r.CreatedAt.IsZero() {
			r.CreatedAt = time.Now().UTC()
		}
		if err = validateReceipt(s, r); err != nil {
			return r, err
		}
		data, e := json.MarshalIndent(r, "", "  ")
		if e != nil {
			return r, e
		}
		if len(data) > maxReceiptBytes {
			return r, errors.New("grant receipt exceeds size limit")
		}
		f, e := os.OpenFile(s.path(r.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			continue
		}
		if e != nil {
			return r, e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			os.Remove(s.path(r.ID))
			return r, e
		}
		return r, nil
	}
	return r, errors.New("could not allocate a unique grant receipt ID")
}

func (s *GrantStore) Get(id string) (GrantReceipt, error) {
	var r GrantReceipt
	if s == nil || !grantIDPattern.MatchString(id) {
		return r, errors.New("invalid grant ID")
	}
	if err := s.secureDir(); err != nil {
		return r, err
	}
	p := s.path(id)
	info, err := os.Lstat(p)
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return r, errors.New("grant receipt is not a regular file")
	}
	if info.Size() > maxReceiptBytes {
		return r, errors.New("grant receipt exceeds size limit")
	}
	f, err := openReceipt(p)
	if err != nil {
		return r, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return r, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxReceiptBytes {
		return r, errors.New("grant receipt is not a bounded regular file")
	}
	if openedInfo.Mode().Perm()&0o077 != 0 {
		return r, errors.New("grant receipt permissions are too broad")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return r, err
	}
	if len(data) > maxReceiptBytes {
		return r, errors.New("grant receipt exceeds size limit")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return r, fmt.Errorf("invalid grant receipt: %w", err)
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return r, errors.New("grant receipt contains trailing data")
	}
	if err = validateReceipt(s, r); err != nil {
		return r, err
	}
	if r.ID != id {
		return r, errors.New("grant receipt ID does not match its filename")
	}
	return r, nil
}

// Export writes a validated local receipt as a private JSON file. The final
// path is created atomically and never replaces an existing file.
func (s *GrantStore) Export(id, path string) error {
	receipt, err := s.Get(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("receipt export path is required")
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxReceiptBytes {
		return errors.New("grant receipt exceeds size limit")
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("receipt export destination already exists; refusing to overwrite")
	} else if !os.IsNotExist(err) {
		return err
	}
	return writePrivateNoReplace(target, data)
}

// ReadExport validates an exported receipt without using or modifying the
// local store directory. A private regular file and exact same-cluster ID are
// required before any caller performs live cluster validation.
func (s *GrantStore) ReadExport(path string) (GrantReceipt, error) {
	var receipt GrantReceipt
	if s == nil || strings.TrimSpace(path) == "" {
		return receipt, errors.New("receipt import path and cluster store are required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return receipt, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return receipt, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return receipt, errors.New("receipt import must be a regular file, not a symlink")
	}
	if info.Size() > maxReceiptBytes {
		return receipt, errors.New("grant receipt exceeds size limit")
	}
	if info.Mode().Perm() != 0600 {
		return receipt, errors.New("receipt import must have mode 0600")
	}
	f, err := openReceipt(absolute)
	if err != nil {
		return receipt, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return receipt, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxReceiptBytes || openedInfo.Mode().Perm() != 0600 {
		return receipt, errors.New("receipt import must be a private, bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReceiptBytes+1))
	if err != nil {
		return receipt, err
	}
	if len(data) > maxReceiptBytes {
		return receipt, errors.New("grant receipt exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return GrantReceipt{}, fmt.Errorf("invalid grant receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return GrantReceipt{}, errors.New("grant receipt contains trailing data")
	}
	if err := validateReceipt(s, receipt); err != nil {
		return GrantReceipt{}, err
	}
	return receipt, nil
}

// Import stores an already validated receipt under its original grant ID.
func (s *GrantStore) Import(receipt GrantReceipt) error {
	if s == nil || s.Dir == "" {
		return errors.New("grant receipt store is unavailable")
	}
	if err := validateReceipt(s, receipt); err != nil {
		return err
	}
	if err := s.secureDir(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	if len(entries) >= maxGrantReceipts {
		return errors.New("grant receipt limit reached; remove old receipts after confirming their grants are gone")
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxReceiptBytes {
		return errors.New("grant receipt exceeds size limit")
	}
	if err := writePrivateNoReplace(s.path(receipt.ID), data); err != nil {
		if os.IsExist(err) {
			return errors.New("grant ID already exists locally; refusing to overwrite")
		}
		return fmt.Errorf("store imported receipt: %w", err)
	}
	return nil
}

func writePrivateNoReplace(target string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".fixora-grant-*.tmp")
	if err != nil {
		return fmt.Errorf("create private receipt file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure private receipt file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write private receipt file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync private receipt file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close private receipt file: %w", err)
	}
	if err := os.Link(tmpPath, target); err != nil {
		return err
	}
	return nil
}

func (s *GrantStore) List() ([]GrantSummary, error) {
	if s == nil {
		return nil, errors.New("grant receipt store is unavailable")
	}
	if err := s.secureDir(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	es, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return []GrantSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(es) > maxGrantReceipts {
		return nil, errors.New("grant receipt store exceeds count limit")
	}
	out := make([]GrantSummary, 0, len(es))
	for _, e := range es {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if !grantIDPattern.MatchString(id) || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		r, er := s.Get(id)
		if er != nil {
			return nil, fmt.Errorf("read grant receipt %s: %w", id, er)
		}
		out = append(out, GrantSummary{ID: r.ID, Namespace: r.Namespace, ServiceAccount: r.ServiceAccount, CreatedAt: r.CreatedAt, Role: r.Role.Name, RoleBinding: r.RoleBinding.Name})
	}
	return out, nil
}

func (s *GrantStore) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return os.Remove(s.path(id))
}
func (s *GrantStore) secureDir() error {
	base := filepath.Dir(filepath.Dir(s.Dir)) // ~/.fixora/<cluster-hash>
	fixoraRoot := filepath.Dir(base)
	for _, dir := range []string{fixoraRoot, base, s.Dir} {
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("grant receipt path contains a non-directory or symlink")
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}
func (s *GrantStore) path(id string) string { return filepath.Join(s.Dir, id+".json") }
func validateReceipt(s *GrantStore, r GrantReceipt) error {
	if r.SchemaVersion != receiptVersion || r.ClusterID != s.ClusterID || !grantIDPattern.MatchString(r.ID) || r.CreatedAt.IsZero() || r.Namespace == "" || r.ServiceAccount == "" || r.ServiceAccountUID == "" || len(r.PolicyDigest) != 64 || r.Role == nil || r.RoleBinding == nil || r.Role.UID == "" || r.RoleBinding.UID == "" || r.Role.Namespace != r.Namespace || r.RoleBinding.Namespace != r.Namespace || r.Role.Name == "" || r.RoleBinding.Name == "" || r.RoleBinding.RoleRef.Kind != "Role" || r.RoleBinding.RoleRef.Name != r.Role.Name || r.RoleBinding.RoleRef.APIGroup != rbacv1.GroupName || len(r.RoleBinding.Subjects) != 1 || r.RoleBinding.Subjects[0].Kind != "ServiceAccount" || r.RoleBinding.Subjects[0].Name != r.ServiceAccount || r.RoleBinding.Subjects[0].Namespace != r.Namespace || len(r.Requests) == 0 {
		return errors.New("grant receipt is incomplete or bound to a different cluster")
	}
	if _, err := hex.DecodeString(r.PolicyDigest); err != nil {
		return errors.New("grant receipt has an invalid policy digest")
	}
	for _, q := range r.Requests {
		if q.Namespace != r.Namespace || q.Resource == "" || q.Verb == "" {
			return errors.New("grant receipt contains an invalid permission request")
		}
	}
	if r.Role.Labels["app.kubernetes.io/managed-by"] != "fixora" || r.RoleBinding.Labels["app.kubernetes.io/managed-by"] != "fixora" || r.Role.Annotations["fixora.io/rbac-policy-sha256"] != r.PolicyDigest || r.RoleBinding.Annotations["fixora.io/rbac-policy-sha256"] != r.PolicyDigest {
		return errors.New("grant receipt does not contain Fixora-owned object markers")
	}
	return nil
}
