package servicefix

import (
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"time"

	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const verifierLabel = "fixora.io/service-verifier"

func temporaryService(p *Proof, nonce string) *corev1.Service {
	s := p.candidate
	out := &corev1.Service{ObjectMeta: metav1.ObjectMeta{GenerateName: "fixora-service-check-", Namespace: s.Namespace, Labels: map[string]string{verifierLabel: nonce}}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{}, Ports: []corev1.ServicePort{s.Spec.Ports[p.portIndex]}, SessionAffinity: corev1.ServiceAffinityNone, IPFamilies: append([]corev1.IPFamily(nil), s.Spec.IPFamilies...)}}
	for k, v := range s.Spec.Selector {
		out.Spec.Selector[k] = v
	}
	if s.Spec.IPFamilyPolicy != nil {
		policy := *s.Spec.IPFamilyPolicy
		out.Spec.IPFamilyPolicy = &policy
	}
	if s.Spec.InternalTrafficPolicy != nil {
		policy := *s.Spec.InternalTrafficPolicy
		out.Spec.InternalTrafficPolicy = &policy
	}
	return out
}
func checkTemporary(expected, got *corev1.Service) error {
	if got == nil || got.UID == "" || got.Name == "" || got.Namespace != expected.Namespace || got.Labels[verifierLabel] != expected.Labels[verifierLabel] || got.Spec.Type != corev1.ServiceTypeClusterIP || got.DeletionTimestamp != nil || !reflect.DeepEqual(got.Spec.Selector, expected.Spec.Selector) || !reflect.DeepEqual(got.Spec.Ports, expected.Spec.Ports) || got.Spec.SessionAffinity != expected.Spec.SessionAffinity || !reflect.DeepEqual(got.Spec.IPFamilies, expected.Spec.IPFamilies) || !reflect.DeepEqual(got.Spec.IPFamilyPolicy, expected.Spec.IPFamilyPolicy) || got.Spec.PublishNotReadyAddresses || len(got.Spec.ExternalIPs) > 0 || got.Spec.ExternalName != "" || !reflect.DeepEqual(got.Spec.InternalTrafficPolicy, expected.Spec.InternalTrafficPolicy) {
		return errors.New("temporary Service admission differs from approved routing")
	}
	ips := got.Spec.ClusterIPs
	if len(ips) == 0 {
		ips = []string{got.Spec.ClusterIP}
	}
	seen := map[string]bool{}
	if len(ips) > 2 {
		return errors.New("unexpected temporary Service address count")
	}
	for _, ip := range ips {
		a, err := netip.ParseAddr(ip)
		if err != nil || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.Zone() != "" || seen[a.String()] {
			return errors.New("invalid temporary Service address")
		}
		seen[a.String()] = true
	}
	return nil
}
func makeProbeRequest(p *Proof, svc *corev1.Service, targets []serviceprobe.Target, nonce string) (serviceprobe.Request, error) {
	req := serviceprobe.Request{Nonce: nonce, Targets: append([]serviceprobe.Target(nil), targets...), Contract: serviceprobe.Contract{Method: p.authority.intent.Spec.Verification.Method, Path: p.authority.intent.Spec.Verification.Path, ExpectedStatus: p.authority.intent.Spec.Verification.ExpectedStatus, Timeout: p.authority.timeout}}
	ips := svc.Spec.ClusterIPs
	if len(ips) == 0 {
		ips = []string{svc.Spec.ClusterIP}
	}
	for _, ip := range ips {
		req.Targets = append(req.Targets, serviceprobe.Target{Address: ip, Port: int(p.authority.intent.Spec.Port.Port)})
	}
	if err := serviceprobe.Validate(req); err != nil {
		return req, err
	}
	return req, nil
}
func probeObjects(p *Proof, req serviceprobe.Request) (*corev1.Pod, *networkingv1.NetworkPolicy, error) {
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > serviceprobe.MaxWireBytes {
		return nil, nil, errors.New("probe request exceeds limit")
	}
	f, t, uid, gid := false, true, int64(65532), int64(65532)
	deadline := int64((3 * time.Minute) / time.Second)
	ns := p.candidate.Namespace
	labels := map[string]string{verifierLabel: req.Nonce}
	protocol := corev1.ProtocolTCP
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "fixora-service-verifier-", Namespace: ns, Labels: labels}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &f, EnableServiceLinks: &f, RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &t, RunAsUser: &uid, RunAsGroup: &gid, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "verifier", Image: p.authority.intent.Spec.Verification.ProbeImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/fixora-service-probe"}, Args: []string{"--request-json", string(raw)}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &f, ReadOnlyRootFilesystem: &t, Privileged: &f, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}}}}
	if p.candidate.Spec.InternalTrafficPolicy != nil && *p.candidate.Spec.InternalTrafficPolicy == corev1.ServiceInternalTrafficPolicyLocal {
		nodes := []string{}
		seen := map[string]bool{}
		for _, b := range p.before.Pods {
			if b.Spec.NodeName != "" && !seen[b.Spec.NodeName] {
				nodes = append(nodes, b.Spec.NodeName)
				seen[b.Spec.NodeName] = true
			}
		}
		if len(nodes) == 0 {
			return nil, nil, errors.New("node-local Service verification requires backend node identity")
		}
		pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: nodes}}}}}}}
	}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{GenerateName: "fixora-service-isolation-", Namespace: ns, Labels: labels}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
	for _, target := range req.Targets {
		a, _ := netip.ParseAddr(target.Address)
		bits := 32
		if a.Is6() {
			bits = 128
		}
		port := intstr.FromInt(target.Port)
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: netip.PrefixFrom(a, bits).String()}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &protocol, Port: &port}}})
	}
	return pod, policy, nil
}
func checkProbe(expected, got *corev1.Pod) error {
	if got == nil || got.UID == "" || got.Name == "" || got.Namespace != expected.Namespace || got.Labels[verifierLabel] != expected.Labels[verifierLabel] || got.DeletionTimestamp != nil || len(got.Spec.Containers) != 1 || len(got.Spec.InitContainers) != 0 || len(got.Spec.EphemeralContainers) != 0 || len(got.Spec.Volumes) != 0 || len(got.Spec.ImagePullSecrets) != 0 || len(got.Spec.HostAliases) != 0 || got.Spec.HostNetwork || got.Spec.HostPID || got.Spec.HostIPC || got.Spec.AutomountServiceAccountToken == nil || *got.Spec.AutomountServiceAccountToken || got.Spec.EnableServiceLinks == nil || *got.Spec.EnableServiceLinks || got.Spec.RestartPolicy != corev1.RestartPolicyNever || got.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != *expected.Spec.ActiveDeadlineSeconds || (got.Spec.ServiceAccountName != "" && got.Spec.ServiceAccountName != "default") || !reflect.DeepEqual(got.Spec.SecurityContext, expected.Spec.SecurityContext) || !reflect.DeepEqual(got.Spec.Affinity, expected.Spec.Affinity) {
		return errors.New("verifier Pod admission changed isolation or identity")
	}
	a, b := expected.Spec.Containers[0], got.Spec.Containers[0]
	if b.Name != a.Name || b.Image != a.Image || b.ImagePullPolicy != a.ImagePullPolicy || !reflect.DeepEqual(b.Command, a.Command) || !reflect.DeepEqual(b.Args, a.Args) || !reflect.DeepEqual(b.SecurityContext, a.SecurityContext) || len(b.Env) != 0 || len(b.EnvFrom) != 0 || len(b.VolumeMounts) != 0 || len(b.VolumeDevices) != 0 || b.Stdin || b.TTY || len(b.Ports) != 0 || b.Lifecycle != nil || b.WorkingDir != a.WorkingDir || b.LivenessProbe != nil || b.ReadinessProbe != nil || b.StartupProbe != nil || !reflect.DeepEqual(b.Resources, a.Resources) {
		return errors.New("verifier binary, contract or security settings changed")
	}
	for k, v := range got.Annotations {
		if strings.Contains(k, "apparmor") || strings.Contains(k, "seccomp") || strings.Contains(k, "istio") || strings.Contains(k, "linkerd") || v == "unconfined" {
			return errors.New("unsupported verifier admission annotation")
		}
	}
	return nil
}
