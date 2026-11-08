package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCleanupDefaultsToPreview(t *testing.T) {
	ns := cleanupCLINamespace("uid-one", cleanupCLITestSession)
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(ns)}
	var stdout, stderr bytes.Buffer
	if code := runCleanup(context.Background(), &stdout, &stderr, options{output: "text"}, client); code != 0 {
		t.Fatalf("cleanup preview exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Preview") || !strings.Contains(stdout.String(), "--yes") || !strings.Contains(stdout.String(), ns.Name) {
		t.Fatalf("preview should name targets and explain explicit deletion: %s", stdout.String())
	}
	if _, err := client.GetNamespace(context.Background(), ns.Name); err != nil {
		t.Fatalf("preview must not delete namespace: %v", err)
	}
}

func TestCleanupWithYesDeletesOnlyEligibleNamespaces(t *testing.T) {
	eligible := cleanupCLINamespace("uid-one", cleanupCLITestSession)
	witness := cleanupCLINamespace("uid-witness", "aaaaaaaa-bbbb-3333-4444-555555555555")
	witness.Annotations["fixora.io/control"] = "network-policy-witness"
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(eligible, witness)}
	var stdout, stderr bytes.Buffer
	if code := runCleanup(context.Background(), &stdout, &stderr, options{output: "text", yes: true}, client); code != 0 {
		t.Fatalf("cleanup exit %d: %s", code, stderr.String())
	}
	if _, err := client.GetNamespace(context.Background(), eligible.Name); err == nil {
		t.Fatal("eligible namespace should be deleted")
	}
	if _, err := client.GetNamespace(context.Background(), witness.Name); err != nil {
		t.Fatalf("NetworkPolicy witness must be preserved: %v", err)
	}
	if !strings.Contains(stdout.String(), "Deleted namespace/") || !strings.Contains(stdout.String(), eligible.Name) {
		t.Fatalf("cleanup should report the deletion result: %s", stdout.String())
	}
}

func TestCleanupPreviewSupportsStructuredOutput(t *testing.T) {
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(cleanupCLINamespace("uid-one", cleanupCLITestSession))}
	var stdout, stderr bytes.Buffer
	if code := runCleanup(context.Background(), &stdout, &stderr, options{output: "json"}, client); code != 0 {
		t.Fatalf("cleanup JSON preview exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "preview"`) || !strings.Contains(stdout.String(), `"noChangesMade": true`) {
		t.Fatalf("JSON output should clearly identify preview with no changes: %s", stdout.String())
	}
}

func TestCleanupListFailureFailsClosed(t *testing.T) {
	clientset := corefake.NewSimpleClientset()
	clientset.PrependReactor("list", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary api failure")
	})
	var stdout, stderr bytes.Buffer
	if code := runCleanup(context.Background(), &stdout, &stderr, options{output: "text", yes: true}, &kube.TypedClient{Clientset: clientset}); code == 0 {
		t.Fatalf("failed candidate discovery must not report cleanup success: %s", stdout.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "list Fixora shadow namespaces") {
		t.Fatalf("list failure should fail closed without claiming deletions, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

const cleanupCLITestSession = "11111111-2222-3333-4444-555555555555"

func cleanupCLINamespace(uid, session string) *corev1.Namespace {
	name := "fixora-shadow-" + strings.ReplaceAll(session, "-", "")[:16]
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name, UID: types.UID(uid), ResourceVersion: "1",
		Labels:      map[string]string{"fixora.io/sandbox": "true", "fixora.io/session": session},
		Annotations: map[string]string{"fixora.io/source-namespace": "payments"},
	}}
}
