package shadow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	shadowSourceNamespaceAnnotation = "fixora.io/source-namespace"
	shadowControlAnnotation         = "fixora.io/control"
	shadowNamespacePrefix           = "fixora-shadow-"
	shadowCleanupPollInterval       = 250 * time.Millisecond
)

// CleanupCandidate is an isolated Fixora shadow run namespace that can be
// safely considered for explicit operator cleanup.
type CleanupCandidate struct {
	Namespace       string `json:"namespace"`
	UID             string `json:"uid"`
	Session         string `json:"session"`
	SourceNamespace string `json:"sourceNamespace"`
}

// ListCleanupCandidates returns only namespaces created for shadow runs.
// NetworkPolicy witness namespaces are deliberately excluded even though they
// share the sandbox label.
func ListCleanupCandidates(ctx context.Context, c *kube.TypedClient) ([]CleanupCandidate, error) {
	if c == nil || c.Clientset == nil {
		return nil, fmt.Errorf("shadow cleanup requires typed Kubernetes client access")
	}
	namespaces, err := c.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: labelSandbox + "=true"})
	if err != nil {
		return nil, fmt.Errorf("list Fixora shadow namespaces: %w", err)
	}
	candidates := make([]CleanupCandidate, 0, len(namespaces.Items))
	for i := range namespaces.Items {
		ns := &namespaces.Items[i]
		if !isCleanupNamespace(ns) || ns.UID == "" {
			continue
		}
		candidates = append(candidates, CleanupCandidate{
			Namespace:       ns.Name,
			UID:             string(ns.UID),
			Session:         ns.Labels[labelSession],
			SourceNamespace: ns.Annotations[shadowSourceNamespaceAnnotation],
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Namespace < candidates[j].Namespace })
	return candidates, nil
}

// DeleteCleanupCandidate revalidates the namespace identity and ownership,
// then deletes it with a Kubernetes UID precondition.
func DeleteCleanupCandidate(ctx context.Context, c *kube.TypedClient, candidate CleanupCandidate) error {
	if c == nil || c.Clientset == nil {
		return fmt.Errorf("shadow cleanup requires typed Kubernetes client access")
	}
	if candidate.Namespace == "" || candidate.UID == "" || candidate.Session == "" || candidate.SourceNamespace == "" {
		return fmt.Errorf("cleanup candidate is missing required identity metadata")
	}
	ns, err := c.GetNamespace(ctx, candidate.Namespace)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("re-read cleanup namespace %s: %w", candidate.Namespace, err)
	}
	if string(ns.UID) != candidate.UID || !isCleanupNamespace(ns) || ns.Labels[labelSession] != candidate.Session || ns.Annotations[shadowSourceNamespaceAnnotation] != candidate.SourceNamespace {
		return fmt.Errorf("namespace %s changed or no longer matches Fixora shadow ownership; refusing deletion", candidate.Namespace)
	}
	if err := c.DeleteNamespaceUIDWithVersion(ctx, candidate.Namespace, types.UID(candidate.UID), ns.ResourceVersion); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete shadow namespace %s: %w", candidate.Namespace, err)
	}
	for {
		current, err := c.GetNamespace(ctx, candidate.Namespace)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("confirm deletion of shadow namespace %s: %w", candidate.Namespace, err)
		}
		if string(current.UID) != candidate.UID {
			// The original namespace is gone. Never wait on a different object
			// that has since reused the same namespace name.
			return nil
		}
		timer := time.NewTimer(shadowCleanupPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for deletion of shadow namespace %s: %w", candidate.Namespace, ctx.Err())
		case <-timer.C:
		}
	}
}

func isCleanupNamespace(ns *corev1.Namespace) bool {
	if ns == nil || !strings.HasPrefix(ns.Name, shadowNamespacePrefix) {
		return false
	}
	session := ns.Labels[labelSession]
	parsedSession, err := uuid.Parse(session)
	return err == nil && parsedSession.String() == session &&
		ns.Name == shadowNamespaceName(session) &&
		ns.Labels[labelSandbox] == "true" &&
		ns.Annotations[shadowSourceNamespaceAnnotation] != "" &&
		ns.Annotations[shadowControlAnnotation] == ""
}
