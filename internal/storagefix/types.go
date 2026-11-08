// Package storagefix contains exact-policy PVC repair candidates and guarded delivery.
package storagefix

import (
	"context"
	"sync"

	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

const (
	ActionSetStorageClass = "setStorageClass"
	ActionExpand          = "expand"
)

type Reader interface {
	GetPVC(context.Context, string, string) (*corev1.PersistentVolumeClaim, error)
	GetStorageClass(context.Context, string) (*storagev1.StorageClass, error)
	GetPV(context.Context, string) (*corev1.PersistentVolume, error)
	ListPods(context.Context, string) ([]corev1.Pod, error)
	ListStatefulSets(context.Context, string) ([]appsv1.StatefulSet, error)
	ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}

type Plan struct {
	Namespace          string                        `json:"namespace"`
	PVCName            string                        `json:"pvc"`
	PVCUID             string                        `json:"pvcUid"`
	PVCResourceVersion string                        `json:"pvcResourceVersion"`
	Action             string                        `json:"action"`
	StorageClass       string                        `json:"storageClass,omitempty"`
	StorageClassUID    string                        `json:"storageClassUid,omitempty"`
	StorageClassRV     string                        `json:"storageClassResourceVersion,omitempty"`
	PVName             string                        `json:"pv,omitempty"`
	PVUID              string                        `json:"pvUid,omitempty"`
	PVResourceVersion  string                        `json:"pvResourceVersion,omitempty"`
	RequestedStorage   string                        `json:"requestedStorage,omitempty"`
	MaximumStorage     string                        `json:"maximumStorage,omitempty"`
	ChangedFields      []string                      `json:"changedFields"`
	Consumers          []corev1.ObjectReference      `json:"consumers,omitempty"`
	PVC                *corev1.PersistentVolumeClaim `json:"-"`
	original           *corev1.PersistentVolumeClaim
	policy             *Authority
	policyDigest       [32]byte
	planDigest         [32]byte
}

type Receipt struct {
	mu         sync.Mutex
	planDigest [32]byte
	pvcUID     string
	pvcRV      string
	consumed   bool
}

type Applied struct {
	Plan
	AppliedResourceVersion string
	AppliedAt              string
}

type Outcome struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}
