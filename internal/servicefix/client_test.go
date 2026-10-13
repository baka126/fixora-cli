package servicefix

import (
	"context"
	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
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
