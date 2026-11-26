package shadow

import (
	"context"
	"testing"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestListCleanupCandidatesSelectsOnlyFixoraRunNamespaces(t *testing.T) {
	client := &kube.TypedClient{Clientset: corefake.NewSimpleClientset(
		cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments"),
		cleanupNamespace("not-sandbox", "uid-2", "session-2", "payments", func(ns *corev1.Namespace) { ns.Labels[labelSandbox] = "false" }),
		cleanupNamespace("missing-session", "uid-3", "", "payments"),
		cleanupNamespace("missing-source", "uid-4", "session-4", ""),
		cleanupNamespace("fixora-shadow-missing-uid", "", cleanupTestSession, "payments"),
		cleanupNamespace("not-fixora-name", "uid-7", "session-7", "payments"),
		cleanupNamespace("fixora-shadow-not-session", "uid-8", cleanupTestSession, "payments"),
		cleanupNamespace("fixora-shadow-prod", "uid-9", "prod", "payments"),
		cleanupNamespace("fixora-shadow-witness", "uid-5", "session-5", "payments", func(ns *corev1.Namespace) { ns.Annotations["fixora.io/control"] = "network-policy-witness" }),
	)}

	candidates, err := ListCleanupCandidates(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Namespace != cleanupTestNamespace || candidates[0].UID != "uid-1" || candidates[0].Session != cleanupTestSession || candidates[0].SourceNamespace != "payments" {
		t.Fatalf("want only the complete Fixora run namespace, got %#v", candidates)
	}
}

func TestDeleteCleanupCandidateRevalidatesOwnershipAndUsesUIDPrecondition(t *testing.T) {
	ns := cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments")
	ns.ResourceVersion = "17"
	clientset := corefake.NewSimpleClientset(ns)
	client := &kube.TypedClient{Clientset: clientset}
	candidate := CleanupCandidate{Namespace: ns.Name, UID: string(ns.UID), Session: cleanupTestSession, SourceNamespace: "payments"}
	if err := DeleteCleanupCandidate(context.Background(), client, candidate); err != nil {
		t.Fatal(err)
	}
	var deleted *ktesting.DeleteActionImpl
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "namespaces" {
			value := action.(ktesting.DeleteActionImpl)
			deleted = &value
		}
	}
	if deleted == nil || deleted.GetDeleteOptions().Preconditions == nil || deleted.GetDeleteOptions().Preconditions.UID == nil || *deleted.GetDeleteOptions().Preconditions.UID != types.UID("uid-1") {
		t.Fatalf("namespace deletion must carry the listed UID precondition, got %#v", deleted)
	}
	if deleted.GetDeleteOptions().Preconditions.ResourceVersion == nil || *deleted.GetDeleteOptions().Preconditions.ResourceVersion != "17" {
		t.Fatalf("namespace deletion must carry the revalidated resource version, got %#v", deleted.GetDeleteOptions().Preconditions)
	}
}

func TestDeleteCleanupCandidateRefusesReplacedNamespace(t *testing.T) {
	ns := cleanupNamespace(cleanupTestNamespace, "new-uid", cleanupTestSession, "payments")
	clientset := corefake.NewSimpleClientset(ns)
	client := &kube.TypedClient{Clientset: clientset}
	candidate := CleanupCandidate{Namespace: ns.Name, UID: "old-uid", Session: cleanupTestSession, SourceNamespace: "payments"}
	if err := DeleteCleanupCandidate(context.Background(), client, candidate); err == nil {
		t.Fatal("replaced namespace must be refused")
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("replaced namespace must not be deleted: %#v", action)
		}
	}
}

func TestDeleteCleanupCandidateRefusesChangedOwnershipMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Namespace)
	}{
		{name: "session", mutate: func(ns *corev1.Namespace) { ns.Labels[labelSession] = "other-session" }},
		{name: "source namespace", mutate: func(ns *corev1.Namespace) { ns.Annotations[shadowSourceNamespaceAnnotation] = "other-source" }},
		{name: "witness control", mutate: func(ns *corev1.Namespace) { ns.Annotations[shadowControlAnnotation] = "network-policy-witness" }},
		{name: "sandbox label", mutate: func(ns *corev1.Namespace) { ns.Labels[labelSandbox] = "false" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments")
			tc.mutate(ns)
			clientset := corefake.NewSimpleClientset(ns)
			candidate := CleanupCandidate{Namespace: ns.Name, UID: string(ns.UID), Session: cleanupTestSession, SourceNamespace: "payments"}
			if err := DeleteCleanupCandidate(context.Background(), &kube.TypedClient{Clientset: clientset}, candidate); err == nil {
				t.Fatal("changed Fixora ownership metadata must be refused")
			}
			for _, action := range clientset.Actions() {
				if action.GetVerb() == "delete" {
					t.Fatalf("changed namespace must not be deleted: %#v", action)
				}
			}
		})
	}
}

func TestDeleteCleanupCandidateWaitsUntilNamespaceIsGone(t *testing.T) {
	ns := cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments")
	ns.ResourceVersion = "17"
	ns.Finalizers = []string{"test.fixora.io/hold"}
	clientset := corefake.NewSimpleClientset(ns)
	clientset.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, nil
	})
	client := &kube.TypedClient{Clientset: clientset}
	candidate := CleanupCandidate{Namespace: ns.Name, UID: string(ns.UID), Session: cleanupTestSession, SourceNamespace: "payments"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := DeleteCleanupCandidate(ctx, client, candidate); err == nil {
		t.Fatal("cleanup should not report completion while a finalizer keeps the namespace")
	}
	if _, err := client.GetNamespace(context.Background(), ns.Name); err != nil {
		t.Fatalf("test finalizer should leave the namespace present: %v", err)
	}
}

func TestDeleteCleanupCandidateRequiresResourceVersion(t *testing.T) {
	ns := cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments")
	clientset := corefake.NewSimpleClientset(ns)
	candidate := CleanupCandidate{Namespace: ns.Name, UID: string(ns.UID), Session: cleanupTestSession, SourceNamespace: "payments"}
	if err := DeleteCleanupCandidate(context.Background(), &kube.TypedClient{Clientset: clientset}, candidate); err == nil {
		t.Fatal("cleanup must refuse deletion when it cannot guard the current resource version")
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatalf("namespace without a resource version must not be deleted: %#v", action)
		}
	}
}

func TestDeleteCleanupCandidateStopsWaitingWhenNameIsReused(t *testing.T) {
	ns := cleanupNamespace(cleanupTestNamespace, "uid-1", cleanupTestSession, "payments")
	ns.ResourceVersion = "17"
	clientset := corefake.NewSimpleClientset(ns)
	clientset.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		resource := corev1.SchemeGroupVersion.WithResource("namespaces")
		if err := clientset.Tracker().Delete(resource, "", ns.Name); err != nil {
			return true, nil, err
		}
		replacement := cleanupNamespace(ns.Name, "new-uid", cleanupTestSession, "payments")
		replacement.ResourceVersion = "18"
		if err := clientset.Tracker().Create(resource, replacement, ""); err != nil {
			return true, nil, err
		}
		return true, nil, nil
	})
	candidate := CleanupCandidate{Namespace: ns.Name, UID: string(ns.UID), Session: cleanupTestSession, SourceNamespace: "payments"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := DeleteCleanupCandidate(ctx, &kube.TypedClient{Clientset: clientset}, candidate); err != nil {
		t.Fatalf("replacement with a new UID should stop the old namespace wait: %v", err)
	}
	replacement, err := clientset.CoreV1().Namespaces().Get(context.Background(), ns.Name, metav1.GetOptions{})
	if err != nil || replacement.UID != "new-uid" {
		t.Fatalf("replacement namespace must be preserved, got %#v err=%v", replacement, err)
	}
}

const (
	cleanupTestSession   = "11111111-2222-3333-4444-555555555555"
	cleanupTestNamespace = "fixora-shadow-1111111122223333"
)

func cleanupNamespace(name, uid, session, source string, mutate ...func(*corev1.Namespace)) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name, UID: types.UID(uid),
		Labels:      map[string]string{labelSandbox: "true", labelSession: session},
		Annotations: map[string]string{"fixora.io/source-namespace": source},
	}}
	for _, fn := range mutate {
		fn(ns)
	}
	return ns
}
