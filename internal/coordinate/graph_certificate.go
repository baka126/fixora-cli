package coordinate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type GraphSourceIdentity struct {
	ID              string                      `json:"id"`
	APIVersion      string                      `json:"apiVersion"`
	Kind            string                      `json:"kind"`
	Namespace       string                      `json:"namespace"`
	Name            string                      `json:"name"`
	UID             string                      `json:"uid"`
	ResourceVersion string                      `json:"resourceVersion"`
	Candidate       string                      `json:"-"`
	Storage         *GraphStorageSourceIdentity `json:"storage,omitempty"`
}

type GraphStorageSourceIdentity struct {
	PVName                             string   `json:"pvName"`
	PVUID                              string   `json:"pvUid"`
	PVResourceVersion                  string   `json:"pvResourceVersion"`
	StorageClassName                   string   `json:"storageClassName"`
	StorageClassUID                    string   `json:"storageClassUid"`
	StorageClassResourceVersion        string   `json:"storageClassResourceVersion"`
	CSIDriver                          string   `json:"csiDriver"`
	AccessModes                        []string `json:"accessModes"`
	VolumeMode                         string   `json:"volumeMode"`
	RequestedBytes                     int64    `json:"requestedBytes"`
	VolumeSnapshotClassName            string   `json:"volumeSnapshotClassName"`
	VolumeSnapshotClassUID             string   `json:"volumeSnapshotClassUid"`
	VolumeSnapshotClassResourceVersion string   `json:"volumeSnapshotClassResourceVersion"`
	RestoreStorageClassName            string   `json:"restoreStorageClassName"`
	RestoreStorageClassUID             string   `json:"restoreStorageClassUid"`
	RestoreStorageClassResourceVersion string   `json:"restoreStorageClassResourceVersion"`
	StoragePolicyDigest                string   `json:"storagePolicyDigest"`
}

func graphStorageSourceIdentity(source *shadow.GraphStorageSource) *GraphStorageSourceIdentity {
	if source == nil {
		return nil
	}
	return &GraphStorageSourceIdentity{PVName: source.PVName, PVUID: source.PVUID, PVResourceVersion: source.PVResourceVersion, StorageClassName: source.StorageClassName, StorageClassUID: source.StorageClassUID, StorageClassResourceVersion: source.StorageClassResourceVersion, CSIDriver: source.CSIDriver, AccessModes: append([]string(nil), source.AccessModes...), VolumeMode: source.VolumeMode, RequestedBytes: source.RequestedBytes, VolumeSnapshotClassName: source.VolumeSnapshotClassName, VolumeSnapshotClassUID: source.VolumeSnapshotClassUID, VolumeSnapshotClassResourceVersion: source.VolumeSnapshotClassResourceVersion, RestoreStorageClassName: source.RestoreStorageClassName, RestoreStorageClassUID: source.RestoreStorageClassUID, RestoreStorageClassResourceVersion: source.RestoreStorageClassResourceVersion, StoragePolicyDigest: source.StoragePolicyDigest}
}

func graphStorageIdentityDigest(s *GraphStorageSourceIdentity) string {
	return strings.Join([]string{s.PVName, s.PVUID, s.PVResourceVersion, s.StorageClassName, s.StorageClassUID, s.StorageClassResourceVersion, s.CSIDriver, s.VolumeMode, fmt.Sprint(s.RequestedBytes), strings.Join(s.AccessModes, ","), s.VolumeSnapshotClassName, s.VolumeSnapshotClassUID, s.VolumeSnapshotClassResourceVersion, s.RestoreStorageClassName, s.RestoreStorageClassUID, s.RestoreStorageClassResourceVersion, s.StoragePolicyDigest}, "|")
}

func completeGraphStorageIdentity(s *GraphStorageSourceIdentity) bool {
	if s == nil || s.PVName == "" || s.PVUID == "" || s.PVResourceVersion == "" || s.StorageClassName == "" || s.StorageClassUID == "" || s.StorageClassResourceVersion == "" || s.CSIDriver == "" || s.VolumeMode != "Filesystem" || s.RequestedBytes <= 0 || s.VolumeSnapshotClassName == "" || s.VolumeSnapshotClassUID == "" || s.VolumeSnapshotClassResourceVersion == "" || s.RestoreStorageClassName == "" || s.RestoreStorageClassUID == "" || s.RestoreStorageClassResourceVersion == "" || s.StoragePolicyDigest == "" {
		return false
	}
	for _, mode := range s.AccessModes {
		if mode == "ReadWriteOnce" {
			return true
		}
	}
	return false
}

type GraphResourceIdentity struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

type GraphSnapshot struct {
	Root                string                `json:"root"`
	Nodes               []GraphSourceIdentity `json:"nodes"`
	Edges               []shadow.GraphEdge    `json:"edges"`
	StoragePolicyDigest string                `json:"storagePolicyDigest,omitempty"`
	Digest              string                `json:"digest"`
}

type graphRuntimeIdentity struct {
	NamespaceUID             string                               `json:"namespaceUid"`
	Namespace                string                               `json:"namespace"`
	NamespaceResourceVersion string                               `json:"namespaceResourceVersion"`
	Workloads                []GraphResourceIdentity              `json:"workloads"`
	Dependencies             []GraphResourceIdentity              `json:"dependencies"`
	SourceDeps               []GraphResourceIdentity              `json:"sourceDependencies"`
	Services                 []GraphResourceIdentity              `json:"services"`
	NetworkPolicies          []GraphResourceIdentity              `json:"networkPolicies"`
	CleanupStatus            string                               `json:"cleanupStatus"`
	HasPersistentData        bool                                 `json:"hasPersistentData"`
	StoragePolicyDigest      string                               `json:"storagePolicyDigest,omitempty"`
	StorageSnapshots         []shadow.GraphStorageSnapshotReceipt `json:"storageSnapshots,omitempty"`
	StorageGrant             *GraphResourceIdentity               `json:"storageGrant,omitempty"`
	StorageSources           []GraphResourceIdentity              `json:"storageSources,omitempty"`
	RestoredClaims           []GraphResourceIdentity              `json:"restoredClaims,omitempty"`
	ClaimReceipts            []shadow.GraphClaimReceipt           `json:"claimReceipts,omitempty"`
	StorageCleanup           *shadow.GraphStorageCleanup          `json:"storageCleanup,omitempty"`
}

// GraphCertificate binds all source candidates and identities to one complete
// shadow receipt. Candidate text is process-local and excluded from JSON.
type GraphCertificate struct {
	Snapshot GraphSnapshot `json:"snapshot"`
	runtime  graphRuntimeIdentity
	seal     string
}

func NewGraphCertificate(graph shadow.DependencyGraph, result shadow.GraphResult) (GraphCertificate, error) {
	if len(graph.Blockers) != 0 || len(result.Blockers) != 0 || !result.Verified || result.Status != shadow.VerificationVerified {
		return GraphCertificate{}, errors.New("complete graph shadow verification is required")
	}
	if result.Namespace == "" || result.NamespaceUID == "" || result.NamespaceResourceVersion == "" {
		return GraphCertificate{}, errors.New("graph shadow namespace identity is incomplete")
	}
	if result.CleanupStatus != "deleted" && result.CleanupStatus != "retained" {
		return GraphCertificate{}, errors.New("graph shadow cleanup status is incomplete")
	}
	if result.HasSecrets && result.CleanupStatus != "deleted" {
		return GraphCertificate{}, errors.New("Secret-bearing graph namespaces must be deleted before certification")
	}
	snapshot, err := GraphSourceSnapshot(graph)
	if err != nil {
		return GraphCertificate{}, err
	}
	if result.GraphDigest == "" || result.GraphDigest != snapshot.Digest {
		return GraphCertificate{}, errors.New("shadow receipt does not match graph candidates and source identities")
	}
	runtime, err := runtimeIdentityFromResult(graph, result)
	if err != nil {
		return GraphCertificate{}, err
	}
	cert := GraphCertificate{Snapshot: snapshot, runtime: runtime}
	cert.seal = graphCertificateSeal(cert.Snapshot, cert.runtime)
	return cert, nil
}

// GraphSourceSnapshot creates a candidate-bound, metadata-only source snapshot.
// Candidate bytes remain in memory and are omitted from JSON output.
func GraphSourceSnapshot(graph shadow.DependencyGraph) (GraphSnapshot, error) {
	snapshot := GraphSnapshot{Root: graph.Root, Edges: append([]shadow.GraphEdge(nil), graph.Edges...)}
	for _, node := range graph.Nodes {
		if node.ID() == "" || node.UID == "" || node.ResourceVersion == "" {
			return GraphSnapshot{}, fmt.Errorf("source identity is incomplete for %s", node.ID())
		}
		if isGraphWorkload(node.Kind) && strings.TrimSpace(node.CandidatePatch) == "" {
			return GraphSnapshot{}, fmt.Errorf("candidate bytes are missing for %s", node.ID())
		}
		identity := GraphSourceIdentity{ID: node.ID(), APIVersion: node.APIVersion, Kind: node.Kind, Namespace: node.Namespace, Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion, Candidate: node.CandidatePatch}
		if node.Kind == "PersistentVolumeClaim" {
			if node.Storage == nil {
				return GraphSnapshot{}, fmt.Errorf("storage identity is incomplete for %s", node.ID())
			}
			identity.Storage = graphStorageSourceIdentity(node.Storage)
			if snapshot.StoragePolicyDigest == "" {
				snapshot.StoragePolicyDigest = node.Storage.StoragePolicyDigest
			} else if snapshot.StoragePolicyDigest != node.Storage.StoragePolicyDigest {
				return GraphSnapshot{}, errors.New("PVC graph has inconsistent storage policy digests")
			}
		}
		snapshot.Nodes = append(snapshot.Nodes, identity)
	}
	canonicalizeGraphSnapshot(&snapshot)
	if err := validateGraphSourceSnapshot(snapshot); err != nil {
		return GraphSnapshot{}, err
	}
	return snapshot, nil
}

// Check verifies the current source graph against the shadow-bound snapshot.
// The graph path intentionally does not call CheckIndependentSteps: connected
// resources are expected here and were exercised together in the shadow run.
func (c GraphCertificate) Check(current GraphSnapshot) error {
	if err := validateGraphSourceSnapshot(c.Snapshot); err != nil {
		return err
	}
	stored := c.Snapshot
	canonicalizeGraphSnapshot(&stored)
	if stored.Digest != c.Snapshot.Digest {
		return errors.New("graph certificate snapshot was mutated")
	}
	if c.seal == "" || c.seal != graphCertificateSeal(c.Snapshot, c.runtime) {
		return errors.New("graph certificate integrity check failed")
	}
	canonicalizeGraphSnapshot(&current)
	if err := validateGraphSourceSnapshot(current); err != nil {
		return fmt.Errorf("current graph snapshot: %w", err)
	}
	if current.Digest != c.Snapshot.Digest || current.Root != c.Snapshot.Root {
		return errors.New("graph candidates or source identities changed after shadow verification")
	}
	return nil
}

func runtimeIdentityFromResult(graph shadow.DependencyGraph, result shadow.GraphResult) (graphRuntimeIdentity, error) {
	wantWorkloads, wantServices, wantDeps := 0, 0, 0
	wantPVCs := 0
	wantedSources := map[string]bool{}
	for _, node := range graph.Nodes {
		if isGraphWorkload(node.Kind) {
			wantWorkloads++
			wantedSources[node.ID()] = true
		}
		if node.Kind == "Service" {
			wantServices++
		}
		if node.Kind == "ConfigMap" || node.Kind == "Secret" {
			wantDeps++
		}
		if node.Kind == "PersistentVolumeClaim" {
			wantPVCs++
		}
	}
	if len(result.Workloads) != wantWorkloads || len(result.Services) != wantServices || len(result.Dependencies) != wantDeps || len(result.SourceDependencies) != wantDeps || len(result.NetworkPolicies) != 2 {
		return graphRuntimeIdentity{}, errors.New("shadow receipt is missing graph workload, dependency, service, or policy evidence")
	}
	wantSecret := false
	wantSourceDeps := map[string]GraphSourceIdentity{}
	wantCopiedDeps := map[string]bool{}
	wantServiceNames := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == "Secret" {
			wantSecret = true
		}
		if node.Kind == "ConfigMap" || node.Kind == "Secret" {
			wantSourceDeps[node.Kind+"/"+node.Name] = GraphSourceIdentity{ID: node.ID(), Kind: node.Kind, Namespace: node.Namespace, Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion}
			wantCopiedDeps[node.Kind+"/"+node.Name] = true
		}
		if node.Kind == "Service" {
			wantServiceNames[node.Name] = true
		}
	}
	if wantSecret != result.HasSecrets {
		return graphRuntimeIdentity{}, errors.New("shadow Secret lifecycle flag differs from graph")
	}
	runtime := graphRuntimeIdentity{Namespace: result.Namespace, NamespaceUID: result.NamespaceUID, NamespaceResourceVersion: result.NamespaceResourceVersion, CleanupStatus: result.CleanupStatus}
	seen := map[string]bool{}
	seenCloneIDs := map[string]bool{}
	for _, workload := range result.Workloads {
		if !wantedSources[workload.Source] || seen[workload.Source] || workload.Pod == "" || workload.PodUID == "" || workload.PodResourceVersion == "" || !workload.Verified || workload.Status != shadow.VerificationVerified {
			return graphRuntimeIdentity{}, errors.New("shadow workload receipt is incomplete or duplicated")
		}
		if seenCloneIDs[workload.Pod] || seenCloneIDs["uid:"+workload.PodUID] {
			return graphRuntimeIdentity{}, errors.New("shadow workload identities are duplicated")
		}
		seenCloneIDs[workload.Pod] = true
		seenCloneIDs["uid:"+workload.PodUID] = true
		seen[workload.Source] = true
		runtime.Workloads = append(runtime.Workloads, GraphResourceIdentity{Kind: "Pod", Namespace: result.Namespace, Name: workload.Pod, UID: workload.PodUID, ResourceVersion: workload.PodResourceVersion})
	}
	seenDeps, seenSourceDeps, seenServices, seenPolicies := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, node := range result.Dependencies {
		key := node.Kind + "/" + node.Name
		if !wantCopiedDeps[key] || seenDeps[key] || node.Namespace != result.Namespace {
			return graphRuntimeIdentity{}, errors.New("copied dependency receipt is unexpected or duplicated")
		}
		seenDeps[key] = true
		runtime.Dependencies = append(runtime.Dependencies, graphResourceIdentity(node))
	}
	for _, node := range result.SourceDependencies {
		key := node.Kind + "/" + node.Name
		want, ok := wantSourceDeps[key]
		if !ok || seenSourceDeps[key] || want.UID != node.UID || want.ResourceVersion != node.ResourceVersion || want.Namespace != node.Namespace {
			return graphRuntimeIdentity{}, errors.New("source dependency receipt is stale or duplicated")
		}
		seenSourceDeps[key] = true
		runtime.SourceDeps = append(runtime.SourceDeps, graphResourceIdentity(node))
	}
	for _, node := range result.Services {
		if !wantServiceNames[node.Name] || seenServices[node.Name] || node.Namespace != result.Namespace {
			return graphRuntimeIdentity{}, errors.New("shadow Service receipt is unexpected or duplicated")
		}
		seenServices[node.Name] = true
		runtime.Services = append(runtime.Services, graphResourceIdentity(node))
	}
	for _, node := range result.NetworkPolicies {
		if node.Kind != "NetworkPolicy" || node.Namespace != result.Namespace || (node.Name != "fixora-default-deny" && node.Name != "fixora-graph-access") || seenPolicies[node.Name] {
			return graphRuntimeIdentity{}, errors.New("NetworkPolicy receipt is unexpected or duplicated")
		}
		seenPolicies[node.Name] = true
		runtime.NetworkPolicies = append(runtime.NetworkPolicies, graphResourceIdentity(node))
	}
	if len(seenDeps) != wantDeps || len(seenSourceDeps) != wantDeps || len(seenServices) != wantServices || len(seenPolicies) != 2 {
		return graphRuntimeIdentity{}, errors.New("shadow receipt has incomplete graph identities")
	}
	if err := bindStorageRuntime(graph, result, &runtime, wantPVCs); err != nil {
		return graphRuntimeIdentity{}, err
	}
	for _, group := range [][]GraphResourceIdentity{runtime.Dependencies, runtime.SourceDeps, runtime.Services, runtime.NetworkPolicies} {
		for _, id := range group {
			if id.Name == "" || id.Namespace == "" || id.UID == "" || id.ResourceVersion == "" {
				return graphRuntimeIdentity{}, errors.New("shadow resource receipt has incomplete identity")
			}
		}
	}
	sortGraphResourceIdentities(runtime.Workloads)
	sortGraphResourceIdentities(runtime.Dependencies)
	sortGraphResourceIdentities(runtime.SourceDeps)
	sortGraphResourceIdentities(runtime.Services)
	sortGraphResourceIdentities(runtime.NetworkPolicies)
	return runtime, nil
}

func graphResourceIdentity(node shadow.GraphNode) GraphResourceIdentity {
	return GraphResourceIdentity{Kind: node.Kind, Namespace: node.Namespace, Name: node.Name, UID: node.UID, ResourceVersion: node.ResourceVersion}
}

func bindStorageRuntime(graph shadow.DependencyGraph, result shadow.GraphResult, runtime *graphRuntimeIdentity, wantPVCs int) error {
	if wantPVCs == 0 {
		if result.HasPersistentData || result.StoragePolicyDigest != "" || len(result.StorageSources) > 0 || len(result.StorageSnapshotReceipts) > 0 || result.StorageGrant != nil || len(result.RestoredClaims) > 0 || len(result.ClaimReceipts) > 0 || result.StorageCleanup != nil {
			return errors.New("storage receipt is unexpected for a non-PVC graph")
		}
		return nil
	}
	if !result.HasPersistentData || result.StoragePolicyDigest == "" || result.CleanupStatus != "deleted" || result.StorageCleanup == nil || !result.StorageCleanup.Complete || len(result.StorageCleanup.Remaining) != 0 {
		return errors.New("persistent graph requires complete deleted storage cleanup")
	}
	if result.StoragePolicyDigest != graphStoragePolicyDigest(graph) {
		return errors.New("storage policy receipt differs from graph snapshot")
	}
	if len(result.StorageSources) != wantPVCs || len(result.StorageSnapshotReceipts) != wantPVCs || len(result.StorageSnapshots) != wantPVCs || len(result.RestoredClaims) != wantPVCs || len(result.ClaimReceipts) != wantPVCs || result.StorageGrant == nil {
		return errors.New("persistent graph receipt is incomplete")
	}
	wanted := map[string]shadow.GraphNode{}
	for _, node := range graph.Nodes {
		if node.Kind == "PersistentVolumeClaim" {
			wanted[node.ID()] = node
		}
	}
	seenSources := map[string]bool{}
	runtime.StorageSources = make([]GraphResourceIdentity, 0, wantPVCs)
	for _, source := range result.StorageSources {
		original, ok := wanted[source.ID()]
		if !ok || seenSources[source.ID()] || source.UID != original.UID || source.ResourceVersion != original.ResourceVersion || source.Storage == nil || original.Storage == nil || source.Storage.DigestString() != original.Storage.DigestString() {
			return errors.New("storage source identity differs from graph snapshot")
		}
		seenSources[source.ID()] = true
		runtime.StorageSources = append(runtime.StorageSources, graphResourceIdentity(source))
	}
	if len(seenSources) != wantPVCs {
		return errors.New("storage source receipt is incomplete")
	}
	if result.StorageGrant.Kind != "ReferenceGrant" || result.StorageGrant.Namespace != graphPVCNamespace(graph) || result.StorageGrant.UID == "" || result.StorageGrant.ResourceVersion == "" {
		return errors.New("temporary ReferenceGrant identity is incomplete")
	}
	runtime.StorageGrant = ptrGraphResourceIdentity(graphResourceIdentity(*result.StorageGrant))
	snapshotIDs := map[string]bool{}
	snapshotsByID := map[string]shadow.GraphNode{}
	for _, snapshot := range result.StorageSnapshots {
		if snapshot.Kind != "VolumeSnapshot" || snapshot.Namespace == "" || snapshot.Name == "" || snapshot.UID == "" || snapshot.ResourceVersion == "" || snapshotIDs[snapshot.Namespace+"/"+snapshot.Name] {
			return errors.New("source snapshot identity is incomplete or duplicated")
		}
		snapshotIDs[snapshot.Namespace+"/"+snapshot.Name] = true
		snapshotsByID[snapshot.Namespace+"/"+snapshot.Name] = snapshot
	}
	seenSnapshots := map[string]bool{}
	for _, receipt := range result.StorageSnapshotReceipts {
		source, ok := wanted[receipt.SourcePVC]
		listed, listedOK := snapshotsByID[receipt.Snapshot.Namespace+"/"+receipt.Snapshot.Name]
		if !ok || seenSnapshots[receipt.SourcePVC] || receipt.Snapshot.Kind != "VolumeSnapshot" || receipt.Snapshot.Namespace != source.Namespace || receipt.Snapshot.Name == "" || receipt.Snapshot.UID == "" || receipt.Snapshot.ResourceVersion == "" || receipt.Content.Kind != "VolumeSnapshotContent" || receipt.Content.Namespace != "" || receipt.Content.Name == "" || receipt.Content.UID == "" || receipt.Content.ResourceVersion == "" || !snapshotIDs[receipt.Snapshot.Namespace+"/"+receipt.Snapshot.Name] || !listedOK || listed.UID != receipt.Snapshot.UID || listed.ResourceVersion != receipt.Snapshot.ResourceVersion {
			return errors.New("snapshot or content receipt is incomplete or unexpected")
		}
		seenSnapshots[receipt.SourcePVC] = true
		runtime.StorageSnapshots = append(runtime.StorageSnapshots, receipt)
	}
	if len(seenSnapshots) != wantPVCs {
		return errors.New("snapshot receipt is incomplete")
	}
	seenRestored := map[string]shadow.GraphNode{}
	for _, claim := range result.RestoredClaims {
		if claim.Kind != "PersistentVolumeClaim" || claim.Namespace != result.Namespace || claim.UID == "" || claim.ResourceVersion == "" || seenRestored[claim.Name].UID != "" {
			return errors.New("restored claim identity is incomplete or duplicated")
		}
		seenRestored[claim.Name] = claim
		runtime.RestoredClaims = append(runtime.RestoredClaims, graphResourceIdentity(claim))
	}
	seenReceipts := map[string]bool{}
	usedRestoredClaims := map[string]bool{}
	for _, receipt := range result.ClaimReceipts {
		source, ok := wanted[receipt.SourcePVC]
		claim, claimOK := seenRestored[receipt.RestoredPVC]
		if !ok || !claimOK || seenReceipts[receipt.SourcePVC] || usedRestoredClaims[receipt.RestoredPVC] || receipt.SourcePVCUID != source.UID || receipt.SourcePVCResourceVersion != source.ResourceVersion || receipt.RestoredPVCUID != claim.UID || receipt.PV == "" || receipt.PVUID == "" || receipt.CSIDriver == "" || source.Storage == nil || receipt.CSIDriver != source.Storage.CSIDriver || receipt.PV == source.Storage.PVName || receipt.PVUID == source.Storage.PVUID || receipt.Node == "" || receipt.NodeUID == "" {
			return errors.New("claim binding or topology receipt is incomplete")
		}
		seenReceipts[receipt.SourcePVC] = true
		usedRestoredClaims[receipt.RestoredPVC] = true
		runtime.ClaimReceipts = append(runtime.ClaimReceipts, receipt)
	}
	if len(seenReceipts) != wantPVCs {
		return errors.New("claim binding proof is incomplete")
	}
	deleted := map[string]bool{}
	for _, item := range result.StorageCleanup.Deleted {
		if item.UID != "" && item.Name != "" {
			deleted[item.Kind+"/"+item.Namespace+"/"+item.Name+"@"+item.UID] = true
		}
	}
	if !deleted[result.StorageGrant.Kind+"/"+result.StorageGrant.Namespace+"/"+result.StorageGrant.Name+"@"+result.StorageGrant.UID] {
		return errors.New("temporary ReferenceGrant deletion is not proven")
	}
	for _, snapshot := range result.StorageSnapshots {
		if !deleted[snapshot.Kind+"/"+snapshot.Namespace+"/"+snapshot.Name+"@"+snapshot.UID] {
			return errors.New("source snapshot deletion is not proven")
		}
	}
	runtime.HasPersistentData = true
	runtime.StoragePolicyDigest = result.StoragePolicyDigest
	copy := *result.StorageCleanup
	runtime.StorageCleanup = &copy
	sort.Slice(runtime.StorageSnapshots, func(i, j int) bool {
		return runtime.StorageSnapshots[i].SourcePVC < runtime.StorageSnapshots[j].SourcePVC
	})
	sortGraphResourceIdentities(runtime.StorageSources)
	sortGraphResourceIdentities(runtime.RestoredClaims)
	sort.Slice(runtime.ClaimReceipts, func(i, j int) bool { return runtime.ClaimReceipts[i].SourcePVC < runtime.ClaimReceipts[j].SourcePVC })
	return nil
}

func graphStoragePolicyDigest(graph shadow.DependencyGraph) string {
	for _, node := range graph.Nodes {
		if node.Kind == "PersistentVolumeClaim" && node.Storage != nil {
			return node.Storage.StoragePolicyDigest
		}
	}
	return ""
}

func graphPVCNamespace(graph shadow.DependencyGraph) string {
	for _, node := range graph.Nodes {
		if node.Kind == "PersistentVolumeClaim" {
			return node.Namespace
		}
	}
	return ""
}

func ptrGraphResourceIdentity(identity GraphResourceIdentity) *GraphResourceIdentity {
	return &identity
}

func canonicalizeGraphSnapshot(snapshot *GraphSnapshot) {
	sort.Slice(snapshot.Nodes, func(i, j int) bool { return snapshot.Nodes[i].ID < snapshot.Nodes[j].ID })
	sort.Slice(snapshot.Edges, func(i, j int) bool {
		a, b := snapshot.Edges[i], snapshot.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.SourceVolumeName < b.SourceVolumeName
	})
	parts := make([]string, 0, len(snapshot.Nodes)+len(snapshot.Edges))
	for _, n := range snapshot.Nodes {
		parts = append(parts, n.ID+"@"+n.UID+":"+n.ResourceVersion+"#"+n.Candidate)
		if n.Storage != nil {
			parts = append(parts, graphStorageIdentityDigest(n.Storage))
		}
	}
	for _, e := range snapshot.Edges {
		parts = append(parts, e.From+">"+string(e.Type)+">"+e.To+"#"+e.SourceVolumeName)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	snapshot.Digest = hex.EncodeToString(sum[:])
}

func validateGraphSourceSnapshot(snapshot GraphSnapshot) error {
	if snapshot.Root == "" || len(snapshot.Nodes) < 2 {
		return errors.New("graph source snapshot is incomplete")
	}
	seen := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.ID == "" || node.UID == "" || node.ResourceVersion == "" {
			return errors.New("graph source identity is incomplete")
		}
		if seen[node.ID] {
			return errors.New("duplicate graph source node")
		}
		seen[node.ID] = true
		if isGraphWorkload(node.Kind) && node.Candidate == "" {
			return fmt.Errorf("candidate bytes are missing for %s", node.ID)
		}
		if node.Kind == "PersistentVolumeClaim" && (!completeGraphStorageIdentity(node.Storage) || snapshot.StoragePolicyDigest == "") {
			return errors.New("graph PVC source identity is incomplete")
		}
		if node.Storage != nil && node.Storage.StoragePolicyDigest != snapshot.StoragePolicyDigest {
			return errors.New("graph PVC policy identity mismatch")
		}
	}
	for _, edge := range snapshot.Edges {
		if !seen[edge.From] || !seen[edge.To] {
			return errors.New("graph edge references a missing node")
		}
	}
	if !seen[snapshot.Root] {
		return errors.New("graph root is missing")
	}
	return nil
}

func isGraphWorkload(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
}
func sortGraphResourceIdentities(items []GraphResourceIdentity) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].Namespace != items[j].Namespace {
			return items[i].Namespace < items[j].Namespace
		}
		return items[i].Name < items[j].Name
	})
}

func graphCertificateSeal(snapshot GraphSnapshot, runtime graphRuntimeIdentity) string {
	var parts []string
	parts = append(parts, snapshot.Root, snapshot.Digest, runtime.Namespace, runtime.NamespaceUID, runtime.NamespaceResourceVersion, runtime.CleanupStatus)
	for _, group := range [][]GraphResourceIdentity{runtime.Workloads, runtime.Dependencies, runtime.SourceDeps, runtime.Services, runtime.NetworkPolicies} {
		for _, id := range group {
			parts = append(parts, fmt.Sprintf("%s/%s/%s@%s:%s", id.Kind, id.Namespace, id.Name, id.UID, id.ResourceVersion))
		}
	}
	storageReceipt, _ := json.Marshal(struct {
		HasPersistentData bool
		PolicyDigest      string
		Snapshots         []shadow.GraphStorageSnapshotReceipt
		Grant             *GraphResourceIdentity
		Sources           []GraphResourceIdentity
		Claims            []GraphResourceIdentity
		ClaimReceipts     []shadow.GraphClaimReceipt
		Cleanup           *shadow.GraphStorageCleanup
	}{runtime.HasPersistentData, runtime.StoragePolicyDigest, runtime.StorageSnapshots, runtime.StorageGrant, runtime.StorageSources, runtime.RestoredClaims, runtime.ClaimReceipts, runtime.StorageCleanup})
	parts = append(parts, string(storageReceipt))
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func (c GraphCertificate) CleanupStatus() string {
	if c.seal == "" || c.seal != graphCertificateSeal(c.Snapshot, c.runtime) {
		return "unknown"
	}
	return c.runtime.CleanupStatus
}
