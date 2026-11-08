package storagefix

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fixora/kubectl-fixora/internal/kube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Runtime interface {
	Reader
	AdmitPVC(context.Context, string, string, []byte) (*corev1.PersistentVolumeClaim, error)
	PatchPVC(context.Context, string, string, []byte) (*corev1.PersistentVolumeClaim, error)
}

type typedRuntime struct{ k *kube.TypedClient }

func NewClient(k *kube.TypedClient) (Runtime, error) {
	if k == nil || k.Clientset == nil {
		return nil, errors.New("PVC repair requires a typed Kubernetes client")
	}
	return &typedRuntime{k: k}, nil
}

func (c *typedRuntime) GetPVC(ctx context.Context, ns, name string) (*corev1.PersistentVolumeClaim, error) {
	return c.k.Clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
}

func (c *typedRuntime) GetStorageClass(ctx context.Context, name string) (*storagev1.StorageClass, error) {
	return c.k.Clientset.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{})
}

func (c *typedRuntime) GetPV(ctx context.Context, name string) (*corev1.PersistentVolume, error) {
	return c.k.Clientset.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
}

func (c *typedRuntime) ListPods(ctx context.Context, ns string) ([]corev1.Pod, error) {
	list, err := c.k.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *typedRuntime) ListStatefulSets(ctx context.Context, ns string) ([]appsv1.StatefulSet, error) {
	list, err := c.k.Clientset.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *typedRuntime) ResolveOwner(ctx context.Context, pod corev1.Pod) (kube.WorkloadOwner, error) {
	b, err := json.Marshal(pod)
	if err != nil {
		return kube.WorkloadOwner{}, err
	}
	var kp kube.Pod
	if err = json.Unmarshal(b, &kp); err != nil {
		return kube.WorkloadOwner{}, err
	}
	return kube.ResolveWorkloadOwner(ctx, c.k, kp)
}

func (c *typedRuntime) AdmitPVC(ctx context.Context, ns, name string, patch []byte) (*corev1.PersistentVolumeClaim, error) {
	return c.k.Clientset.CoreV1().PersistentVolumeClaims(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "fixora-storage-verification"})
}

func (c *typedRuntime) PatchPVC(ctx context.Context, ns, name string, patch []byte) (*corev1.PersistentVolumeClaim, error) {
	return c.k.Clientset.CoreV1().PersistentVolumeClaims(ns).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{FieldManager: "kubectl-fixora-storage"})
}
