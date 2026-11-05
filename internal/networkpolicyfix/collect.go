package networkpolicyfix

import (
	"context"
	"errors"

	"github.com/fixora/kubectl-fixora/internal/kube"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Reader interface {
	GetPolicy(context.Context, string, string) (*networkingv1.NetworkPolicy, error)
	ListPolicies(context.Context, string) ([]networkingv1.NetworkPolicy, error)
	ListPods(context.Context, string) ([]corev1.Pod, error)
	GetNamespace(context.Context, string) (*corev1.Namespace, error)
	GetWorkload(context.Context, string, string, string) (map[string]any, error)
	ResolveOwner(context.Context, corev1.Pod) (kube.WorkloadOwner, error)
}

type EvidenceIssue struct {
	Code     string `json:"code"`
	Resource string `json:"resource"`
	Detail   string `json:"detail,omitempty"`
}

type Snapshot struct {
	Policy    *networkingv1.NetworkPolicy   `json:"policy,omitempty"`
	Policies  []networkingv1.NetworkPolicy  `json:"policies,omitempty"`
	Namespace *corev1.Namespace             `json:"namespace,omitempty"`
	Workload  map[string]any                `json:"workload,omitempty"`
	Pods      []corev1.Pod                  `json:"pods,omitempty"`
	Owners    map[string]kube.WorkloadOwner `json:"owners,omitempty"`
	Issues    []EvidenceIssue               `json:"issues,omitempty"`
}

type Diagnosis struct {
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Cause    string `json:"cause"`
	Next     string `json:"next"`
}

func Collect(ctx context.Context, reader Reader, namespace, name string) (Snapshot, error) {
	if reader == nil || namespace == "" || name == "" {
		return Snapshot{}, errors.New("NetworkPolicy reader and exact target required")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if policy, err := reader.GetPolicy(ctx, namespace, name); err != nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "PolicyUnreadable", Resource: "NetworkPolicy/" + name, Detail: safeError(err)})
	} else if policy == nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "PolicyNotFound", Resource: "NetworkPolicy/" + name})
	} else {
		snapshot.Policy = policy.DeepCopy()
	}
	if policies, err := reader.ListPolicies(ctx, namespace); err != nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "PolicyInventoryUnavailable", Resource: "NetworkPolicy", Detail: safeError(err)})
	} else {
		snapshot.Policies = make([]networkingv1.NetworkPolicy, len(policies))
		for i := range policies {
			policies[i].DeepCopyInto(&snapshot.Policies[i])
		}
	}
	if pods, err := reader.ListPods(ctx, namespace); err != nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "PodInventoryUnavailable", Resource: "Pod", Detail: safeError(err)})
	} else {
		snapshot.Pods = make([]corev1.Pod, len(pods))
		for i := range pods {
			pods[i].DeepCopyInto(&snapshot.Pods[i])
		}
	}
	if ns, err := reader.GetNamespace(ctx, namespace); err != nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "NamespaceUnreadable", Resource: "Namespace/" + namespace, Detail: safeError(err)})
	} else if ns == nil {
		snapshot.Issues = append(snapshot.Issues, EvidenceIssue{Code: "NamespaceNotFound", Resource: "Namespace/" + namespace})
	} else {
		snapshot.Namespace = ns.DeepCopy()
	}
	return snapshot, nil
}

func hasIssue(issues []EvidenceIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func Diagnose(snapshot Snapshot) []Diagnosis {
	var out []Diagnosis
	if snapshot.Policy == nil {
		for _, issue := range snapshot.Issues {
			out = append(out, Diagnosis{Status: issue.Code, Severity: "high", Cause: "NetworkPolicy evidence is incomplete.", Next: "Restore read access and inspect the exact target before proposing a change."})
		}
		return out
	}
	spec := snapshot.Policy.Spec
	if !selectorNonEmpty(spec.PodSelector) {
		out = append(out, Diagnosis{Status: "EmptyPodSelector", Severity: "medium", Cause: "The policy applies to every Pod in its namespace.", Next: "Confirm the intended workload selector before replacing this policy."})
	} else if _, err := metav1.LabelSelectorAsSelector(&spec.PodSelector); err != nil {
		out = append(out, Diagnosis{Status: "InvalidPodSelector", Severity: "high", Cause: "The policy Pod selector is malformed.", Next: "Review the selector syntax in the source of truth before changing it."})
	} else if !hasIssue(snapshot.Issues, "PodInventoryUnavailable") {
		selector, _ := metav1.LabelSelectorAsSelector(&spec.PodSelector)
		matched := false
		for _, pod := range snapshot.Pods {
			if pod.Namespace == snapshot.Policy.Namespace && selector.Matches(labels.Set(pod.Labels)) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, Diagnosis{Status: "NoSelectedPods", Severity: "low", Cause: "The policy selector currently matches no readable Pods.", Next: "Confirm the intended workload and labels before replacing the selector."})
		}
	}
	if hasIssue(snapshot.Issues, "PodInventoryUnavailable") {
		out = append(out, Diagnosis{Status: "PodListUnavailable", Severity: "low", Cause: "Pod inventory could not be read, so policy selection is uncertain.", Next: "Restore Pod list access before evaluating policy coverage."})
	}
	if hasIssue(snapshot.Issues, "PolicyInventoryUnavailable") {
		out = append(out, Diagnosis{Status: "PolicyInventoryUnavailable", Severity: "low", Cause: "The namespace NetworkPolicy inventory could not be read, so additive policy effects are uncertain.", Next: "Restore NetworkPolicy list access before evaluating whether this policy can be changed."})
	}
	if hasIssue(snapshot.Issues, "NamespaceUnreadable") || hasIssue(snapshot.Issues, "NamespaceNotFound") {
		out = append(out, Diagnosis{Status: "NamespaceUnavailable", Severity: "low", Cause: "Namespace identity and labels could not be established.", Next: "Restore namespace read access before evaluating policy selectors."})
	}
	if !hasIssue(snapshot.Issues, "PolicyInventoryUnavailable") && !hasIssue(snapshot.Issues, "PodInventoryUnavailable") {
		overlapping := map[string]bool{}
		for _, pod := range snapshot.Pods {
			if pod.Namespace != snapshot.Policy.Namespace {
				continue
			}
			count := 0
			for _, policy := range snapshot.Policies {
				if policy.Namespace != pod.Namespace {
					continue
				}
				selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
				if err == nil && selector.Matches(labels.Set(pod.Labels)) {
					count++
				}
			}
			if count > 1 {
				for _, policy := range snapshot.Policies {
					if policy.Namespace != pod.Namespace {
						continue
					}
					selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
					if err == nil && selector.Matches(labels.Set(pod.Labels)) {
						overlapping[policy.Name] = true
					}
				}
			}
		}
		if len(overlapping) > 0 {
			out = append(out, Diagnosis{Status: "OverlappingPolicies", Severity: "medium", Cause: "Multiple NetworkPolicies select the same Pod; allowed traffic is additive.", Next: "Review every policy selecting the workload before changing one policy."})
		}
	}
	return out
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
