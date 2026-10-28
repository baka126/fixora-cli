package shadow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"regexp"
)

var pinnedImageDigest = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)

func validateProbeImage(image string) error {
	image = strings.TrimSpace(image)
	if !pinnedImageDigest.MatchString(image) {
		return fmt.Errorf("a digest-pinned shadow probe image is required (set FIXORA_SHADOW_PROBE_IMAGE to image@sha256:<64 lowercase hex characters>)")
	}
	return nil
}

func sandboxDependencyLimitations(pod *corev1.Pod) []string {
	if pod == nil {
		return []string{"source Pod template is unavailable"}
	}
	limitations := []string{}
	if len(pod.Spec.ImagePullSecrets) > 0 {
		limitations = append(limitations, "image pull secret references are not copied into the isolated namespace")
	}
	configMapDependency := false
	for _, volume := range pod.Spec.Volumes {
		if volume.ConfigMap != nil && (volume.ConfigMap.Optional == nil || !*volume.ConfigMap.Optional) {
			configMapDependency = true
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil && (source.ConfigMap.Optional == nil || !*source.ConfigMap.Optional) {
					configMapDependency = true
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...)
	for _, container := range containers {
		for _, source := range container.EnvFrom {
			if source.ConfigMapRef != nil && (source.ConfigMapRef.Optional == nil || !*source.ConfigMapRef.Optional) {
				configMapDependency = true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil && (env.ValueFrom.ConfigMapKeyRef.Optional == nil || !*env.ValueFrom.ConfigMapKeyRef.Optional) {
				configMapDependency = true
			}
		}
	}
	if configMapDependency {
		limitations = append(limitations, "required ConfigMap references are not copied into the isolated namespace")
	}
	return limitations
}

func inspectSourceServiceAccount(ctx context.Context, c *kube.TypedClient, namespace string, pod *corev1.Pod) (bool, error) {
	if c == nil || c.Clientset == nil {
		return false, fmt.Errorf("typed Kubernetes client is unavailable for service-account inspection")
	}
	name := "default"
	if pod != nil && strings.TrimSpace(pod.Spec.ServiceAccountName) != "" {
		name = pod.Spec.ServiceAccountName
	}
	account, err := c.Clientset.CoreV1().ServiceAccounts(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(account.ImagePullSecrets) != 0, nil
}

func unsupportedNetworkPolicyResources(resources []*metav1.APIResourceList) []string {
	var unsupported []string
	for _, list := range resources {
		if list == nil {
			continue
		}
		for _, resource := range list.APIResources {
			kind := strings.ToLower(resource.Kind)
			name := strings.ToLower(resource.Name)
			if !strings.Contains(kind, "networkpolicy") && !strings.Contains(name, "networkpolicy") {
				continue
			}
			if list.GroupVersion == "networking.k8s.io/v1" && resource.Kind == "NetworkPolicy" && resource.Namespaced {
				continue
			}
			unsupported = append(unsupported, list.GroupVersion+"/"+resource.Kind)
		}
	}
	return unsupported
}

func activeUnsupportedNetworkPolicies(ctx context.Context, c *kube.TypedClient, resources []*metav1.APIResourceList, namespaces ...string) ([]string, error) {
	if c == nil || c.Dynamic == nil {
		return nil, fmt.Errorf("dynamic Kubernetes client is unavailable for complete policy inventory")
	}
	var active []string
	for _, list := range resources {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("invalid discovered policy API version %q: %w", list.GroupVersion, err)
		}
		for _, resource := range list.APIResources {
			kind, name := strings.ToLower(resource.Kind), strings.ToLower(resource.Name)
			if (!strings.Contains(kind, "networkpolicy") && !strings.Contains(name, "networkpolicy")) || strings.Contains(name, "/") {
				continue
			}
			if list.GroupVersion == "networking.k8s.io/v1" && resource.Kind == "NetworkPolicy" && resource.Namespaced {
				continue
			}
			gvr := gv.WithResource(resource.Name)
			if resource.Namespaced {
				for _, namespace := range namespaces {
					policies, err := c.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
					if err != nil {
						return nil, fmt.Errorf("cannot inventory %s in namespace %s: %w", gvr.String(), namespace, err)
					}
					if len(policies.Items) > 0 {
						active = append(active, gvr.String()+" in "+namespace)
					}
				}
			} else {
				policies, err := c.Dynamic.Resource(gvr).List(ctx, metav1.ListOptions{})
				if err != nil {
					return nil, fmt.Errorf("cannot inventory cluster-scoped %s: %w", gvr.String(), err)
				}
				if len(policies.Items) > 0 {
					active = append(active, gvr.String())
				}
			}
		}
	}
	sort.Strings(active)
	return active, nil
}

func ensureSandboxNetworkPolicy(ctx context.Context, c *kube.TypedClient, expected *networkingv1.NetworkPolicy) error {
	if c == nil || c.Clientset == nil || expected == nil || expected.UID == "" {
		return fmt.Errorf("sandbox NetworkPolicy identity is incomplete")
	}
	policies, err := c.Clientset.NetworkingV1().NetworkPolicies(expected.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("cannot recheck sandbox NetworkPolicy: %w", err)
	}
	if len(policies.Items) != 1 {
		return fmt.Errorf("sandbox namespace does not contain exactly one NetworkPolicy")
	}
	actual := &policies.Items[0]
	if actual.Name != expected.Name || actual.UID != expected.UID || !isShadowObject(actual.Labels) || actual.Labels[labelSession] != expected.Labels[labelSession] {
		return fmt.Errorf("sandbox NetworkPolicy ownership or UID changed")
	}
	if len(actual.Spec.Ingress) != 0 || len(actual.Spec.Egress) != 0 || !apiequality.Semantic.DeepEqual(actual.Spec.PolicyTypes, expected.Spec.PolicyTypes) || !apiequality.Semantic.DeepEqual(actual.Spec.PodSelector, expected.Spec.PodSelector) {
		return fmt.Errorf("sandbox NetworkPolicy no longer denies both directions")
	}
	return nil
}

type gateFailure struct {
	status VerificationStatus
	err    error
}

func (e gateFailure) Error() string { return e.err.Error() }

func proveNetworkPolicy(ctx context.Context, c *kube.TypedClient, sandbox *corev1.Namespace, policy *networkingv1.NetworkPolicy, session, image string) (report IsolationReport, err error) {
	report = IsolationReport{Status: string(VerificationInconclusive), Summary: "NetworkPolicy enforcement was not proven"}
	defer func() {
		if failure, ok := err.(gateFailure); ok {
			report.Status = string(failure.status)
			report.Summary = failure.Error()
		}
	}()
	gateCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	if c.Discovery == nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("Kubernetes API discovery is unavailable; cannot rule out cluster-wide network policies")}
	}
	_, resources, discoveryErr := c.Discovery.ServerGroupsAndResources()
	if discoveryErr != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("network policy API inventory is incomplete: %w", discoveryErr)}
	}
	active, inventoryErr := activeUnsupportedNetworkPolicies(gateCtx, c, resources, sandbox.Name)
	if inventoryErr != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("nonstandard network policy inventory is incomplete: %w", inventoryErr)}
	}
	if len(active) != 0 {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("active nonstandard or cluster-wide NetworkPolicies can affect isolation: %s", strings.Join(active, ", "))}
	}
	if sandbox == nil || sandbox.UID == "" || !isShadowObject(sandbox.Labels) {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("sandbox namespace ownership could not be proven")}
	}
	preexisting, err := c.Clientset.NetworkingV1().NetworkPolicies(sandbox.Name).List(gateCtx, metav1.ListOptions{})
	if err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("cannot inspect sandbox NetworkPolicies: %w", err)}
	}
	if len(preexisting.Items) != 0 {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("sandbox namespace has pre-existing NetworkPolicies")}
	}
	witnessName := sandbox.Name + "-witness"
	witness, err := c.CreateNamespace(gateCtx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        witnessName,
		Labels:      map[string]string{labelSandbox: "true", labelSession: session},
		Annotations: map[string]string{"fixora.io/control": "network-policy-witness"},
	}})
	if err != nil {
		if existing, getErr := c.GetNamespace(gateCtx, witnessName); getErr == nil && existing.Labels[labelSandbox] == "true" && existing.Labels[labelSession] == session && existing.Annotations["fixora.io/control"] == "network-policy-witness" && existing.UID != "" {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			cleanupErr := deleteOwnedNamespace(cleanupCtx, c, existing)
			cleanupCancel()
			if cleanupErr != nil {
				return report, gateFailure{VerificationBlocked, fmt.Errorf("witness namespace create was ambiguous and cleanup failed: %w", cleanupErr)}
			}
		}
		return report, gateFailure{VerificationBlocked, fmt.Errorf("create NetworkPolicy witness namespace: %w", err)}
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if cleanupErr := deleteOwnedNamespace(cleanupCtx, c, witness); cleanupErr != nil {
			report = IsolationReport{Status: string(VerificationBlocked), Summary: "NetworkPolicy witness cleanup failed"}
			if err != nil {
				err = gateFailure{VerificationBlocked, fmt.Errorf("%v; witness cleanup failed: %w", err, cleanupErr)}
			} else {
				err = gateFailure{VerificationBlocked, fmt.Errorf("witness cleanup failed: %w", cleanupErr)}
			}
		}
	}()
	if witness.UID == "" {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("API server did not return a witness namespace UID")}
	}
	witnessPolicies, err := c.Clientset.NetworkingV1().NetworkPolicies(witness.Name).List(gateCtx, metav1.ListOptions{})
	if err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("cannot inspect witness NetworkPolicies: %w", err)}
	}
	if len(witnessPolicies.Items) != 0 {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("witness namespace has pre-existing NetworkPolicies")}
	}
	active, inventoryErr = activeUnsupportedNetworkPolicies(gateCtx, c, resources, sandbox.Name, witness.Name)
	if inventoryErr != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("nonstandard witness policy inventory is incomplete: %w", inventoryErr)}
	}
	if len(active) != 0 {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("active nonstandard NetworkPolicies affect witness namespaces: %s", strings.Join(active, ", "))}
	}
	listenerSandbox := isolationListenerPod(sandbox.Name, session, image)
	listenerWitness := isolationListenerPod(witness.Name, session, image)
	listenerSandbox, err = c.CreatePod(gateCtx, listenerSandbox)
	if err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("create sandbox NetworkPolicy witness: %w", err)}
	}
	listenerWitness, err = c.CreatePod(gateCtx, listenerWitness)
	if err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("create external NetworkPolicy witness: %w", err)}
	}
	if err = waitPodReady(gateCtx, c, listenerSandbox); err != nil {
		return report, gateFailure{VerificationInconclusive, fmt.Errorf("sandbox NetworkPolicy witness did not become ready: %w", err)}
	}
	if err = waitPodReady(gateCtx, c, listenerWitness); err != nil {
		return report, gateFailure{VerificationInconclusive, fmt.Errorf("external NetworkPolicy witness did not become ready: %w", err)}
	}
	if err = runIsolationProbe(gateCtx, c, sandbox.Name, session, image, listenerWitness, true); err != nil {
		return report, gateFailure{VerificationInconclusive, fmt.Errorf("positive sandbox egress control failed: %w", err)}
	}
	if err = runIsolationProbe(gateCtx, c, witness.Name, session, image, listenerSandbox, true); err != nil {
		return report, gateFailure{VerificationInconclusive, fmt.Errorf("positive sandbox ingress control failed: %w", err)}
	}
	report.Checks = append(report.Checks, "unrestricted sandbox egress and ingress controls passed")
	createdPolicy, createErr := c.CreateNetworkPolicy(gateCtx, policy)
	if createErr != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("create default-deny sandbox NetworkPolicy: %w", createErr)}
	}
	*policy = *createdPolicy.DeepCopy()
	if err = runIsolationProbe(gateCtx, c, sandbox.Name, session, image, listenerWitness, false); err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("sandbox egress deny control was inconclusive: %w", err)}
	}
	if err = runIsolationProbe(gateCtx, c, witness.Name, session, image, listenerSandbox, false); err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("sandbox ingress deny control was inconclusive: %w", err)}
	}
	if err = ensureSandboxNetworkPolicy(gateCtx, c, policy); err != nil {
		return report, gateFailure{VerificationBlocked, err}
	}
	active, inventoryErr = activeUnsupportedNetworkPolicies(gateCtx, c, resources, sandbox.Name, witness.Name)
	if inventoryErr != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("final network policy inventory is incomplete: %w", inventoryErr)}
	}
	if len(active) != 0 {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("active nonstandard NetworkPolicies appeared during isolation controls: %s", strings.Join(active, ", "))}
	}
	if err = deleteOwnedPod(gateCtx, c, listenerSandbox); err != nil {
		return report, gateFailure{VerificationBlocked, fmt.Errorf("remove sandbox control pod: %w", err)}
	}
	report.Status = string(VerificationVerified)
	report.Summary = "default-deny ingress and egress enforcement proven"
	report.Checks = append(report.Checks, "sandbox egress denied", "sandbox ingress denied", "temporary witness namespace removed")
	return report, nil
}

func isolationListenerPod(namespace, session, image string) *corev1.Pod {
	return isolationPod(namespace, "listener", session, image, []string{"--listen"}, []corev1.ContainerPort{{Name: "probe", ContainerPort: serviceprobe.IsolationListenerPort, Protocol: corev1.ProtocolTCP}})
}

func isolationPod(namespace, role, session, image string, args []string, ports []corev1.ContainerPort) *corev1.Pod {
	falseValue := false
	trueValue := true
	user := int64(65532)
	deadline := int64(25)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fixora-" + role + "-" + strings.ReplaceAll(session[:8], "-", ""),
			Namespace: namespace,
			Labels:    map[string]string{labelSandbox: "true", labelSession: session, "fixora.io/control": role},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:        &deadline,
			AutomountServiceAccountToken: &falseValue,
			EnableServiceLinks:           &falseValue,
			DNSPolicy:                    corev1.DNSNone,
			Containers: []corev1.Container{{
				Name: "probe", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Args: args, Ports: ports,
				SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &trueValue, RunAsUser: &user, AllowPrivilegeEscalation: &falseValue, ReadOnlyRootFilesystem: &trueValue, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("8Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("32Mi")}},
			}},
		},
	}
}

func runIsolationProbe(ctx context.Context, c *kube.TypedClient, namespace, session, image string, target *corev1.Pod, expectConnect bool) error {
	address, err := netip.ParseAddr(target.Status.PodIP)
	if err != nil || !address.IsValid() || address.IsUnspecified() || address.IsMulticast() {
		return fmt.Errorf("witness pod has no usable IP address")
	}
	nonce := strings.ReplaceAll(uuid.NewString(), "-", "")
	request := serviceprobe.Request{Nonce: nonce, Targets: []serviceprobe.Target{{Address: address.String(), Port: serviceprobe.IsolationListenerPort, PodUID: string(target.UID)}}, Contract: serviceprobe.Contract{Method: "tcp", Timeout: 2 * time.Second}}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	client := isolationPod(namespace, "probe", session, image, []string{"--request-json=" + string(data)}, nil)
	created, err := c.CreatePod(ctx, client)
	if err != nil {
		return fmt.Errorf("create control probe: %w", err)
	}
	exitCode, waitErr := waitPodExit(ctx, c, created)
	deleteErr := deleteOwnedPod(ctx, c, created)
	if waitErr != nil {
		return waitErr
	}
	if deleteErr != nil {
		return deleteErr
	}
	connected := exitCode == 0
	if connected != expectConnect {
		if expectConnect {
			return fmt.Errorf("expected TCP connectivity, probe exited %d", exitCode)
		}
		return fmt.Errorf("TCP connectivity still works after default-deny policy")
	}
	return nil
}

func waitPodReady(ctx context.Context, c *kube.TypedClient, pod *corev1.Pod) error {
	for {
		current, err := c.GetTypedPod(ctx, pod.Namespace, pod.Name)
		if err == nil {
			pod.Status = current.Status
			if current.Status.Phase == corev1.PodRunning && podReady(current) {
				return nil
			}
			if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
				return fmt.Errorf("control pod terminated before ready")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func waitPodExit(ctx context.Context, c *kube.TypedClient, pod *corev1.Pod) (int32, error) {
	for {
		current, err := c.GetTypedPod(ctx, pod.Namespace, pod.Name)
		if err == nil && (current.Status.Phase == corev1.PodSucceeded || current.Status.Phase == corev1.PodFailed) {
			for _, status := range current.Status.ContainerStatuses {
				if status.Name == "probe" && status.State.Terminated != nil {
					return status.State.Terminated.ExitCode, nil
				}
			}
			return 0, fmt.Errorf("control probe stopped without a container exit status")
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("control probe did not finish: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func deleteOwnedPod(ctx context.Context, c *kube.TypedClient, pod *corev1.Pod) error {
	current, err := c.GetTypedPod(ctx, pod.Namespace, pod.Name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.UID == "" || current.UID != pod.UID || current.Labels[labelSandbox] != "true" || current.Labels[labelSession] != pod.Labels[labelSession] {
		return fmt.Errorf("ownership markers did not match")
	}
	if err := c.DeletePodUID(ctx, current.Namespace, current.Name, types.UID(current.UID)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		_, err := c.GetTypedPod(ctx, current.Namespace, current.Name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pod deletion did not complete before timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func deleteOwnedNamespace(ctx context.Context, c *kube.TypedClient, namespace *corev1.Namespace) error {
	current, err := c.GetNamespace(ctx, namespace.Name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.UID == "" || current.UID != namespace.UID || !isShadowObject(current.Labels) {
		return fmt.Errorf("namespace ownership markers did not match")
	}
	if err := c.DeleteNamespaceUID(ctx, current.Name, types.UID(current.UID)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		_, err := c.GetNamespace(ctx, current.Name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("namespace deletion did not complete before timeout")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
