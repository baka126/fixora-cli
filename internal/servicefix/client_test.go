package servicefix

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"testing"
	"time"
)

func TestServiceClientRequiresTypedStack(t *testing.T) {
	if _, err := NewClient(nil); err == nil {
		t.Fatal("nil client")
	}
	if _, err := NewClient(&kube.TypedClient{}); err == nil {
		t.Fatal("fallback client")
	}
}
func TestServiceClientDeleteUIDPreconditions(t *testing.T) {
	k := fake.NewSimpleClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "temporary", Namespace: "sample", UID: "new"}})
	r, err := NewClient(&kube.TypedClient{Clientset: k})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.DeleteOwned(context.Background(), ObjectRef{Kind: "Service", Namespace: "sample", Name: "temporary", UID: "old"}); err == nil {
		t.Fatal("replacement deleted")
	}
	if _, err = k.CoreV1().Services("sample").Get(context.Background(), "temporary", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceClientWaitsForActualCleanup(t *testing.T) {
	k := fake.NewSimpleClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "temporary", Namespace: "sample", UID: "owned"}})
	k.PrependReactor("delete", "services", func(action ktesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
	r, err := NewClient(&kube.TypedClient{Clientset: k})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err = r.DeleteOwned(ctx, ObjectRef{"Service", "sample", "temporary", "owned"}); err == nil {
		t.Fatal("deletion acceptance certified as completed cleanup")
	}
}
