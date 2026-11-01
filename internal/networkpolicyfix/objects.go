package networkpolicyfix

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	runMarkerAnnotation = "fixora.io/networkpolicy-verification"
	runRoleAnnotation   = "fixora.io/networkpolicy-verification-role"
	caseAnnotation      = "fixora.io/networkpolicy-verification-case"
)

type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	RunMarker string `json:"runMarker"`
}

type pendingObject struct {
	ref      ObjectRef
	resolved bool
}

func randomMarker() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func objectName(marker string, index int, role string) string {
	if len(marker) > 12 {
		marker = marker[:12]
	}
	role = strings.ToLower(strings.Trim(role, "-"))
	if len(role) > 14 {
		role = role[:14]
	}
	return fmt.Sprintf("npv-%s-%s-%02d", marker, role, index)
}

func scenarioNamespaces(marker string, index int, sourceLabels, destinationLabels map[string]string) (*corev1.Namespace, *corev1.Namespace) {
	annotations := map[string]string{runMarkerAnnotation: marker}
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: objectName(marker, index, "src"), Labels: cloneLabels(sourceLabels), Annotations: cloneLabels(annotations)}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: objectName(marker, index, "dst"), Labels: cloneLabels(destinationLabels), Annotations: cloneLabels(annotations)}}
}

func sandboxPolicy(namespace, name, marker, stage string, spec networkingv1.NetworkPolicySpec) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{runMarkerAnnotation: marker, caseAnnotation: stage}}, Spec: *spec.DeepCopy()}
}

func denyPolicy(namespace, name, marker string, selector map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Annotations: map[string]string{runMarkerAnnotation: marker}}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: cloneLabels(selector)}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}}
}

func probePod(namespace, name, marker, role, stage, image string, labelsMap map[string]string, port int32, responseNonce string, timeout time.Duration, requestJSON string) *corev1.Pod {
	falseValue, trueValue := false, true
	uid, gid := int64(65532), int64(65532)
	deadline := int64(30)
	if timeout > 0 && timeout.Seconds()*4 > 30 {
		deadline = int64(timeout.Seconds() * 4)
		if deadline > 120 {
			deadline = 120
		}
	}
	seccomp := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	annotations := map[string]string{runMarkerAnnotation: marker, runRoleAnnotation: role, caseAnnotation: stage}
	container := corev1.Container{Name: "probe", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &falseValue, ReadOnlyRootFilesystem: &trueValue, Privileged: &falseValue, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}
	if role == "server" {
		container.Command = []string{"/fixora-service-probe"}
		container.Args = []string{"--listen", fmt.Sprintf(":%d", port), "--response-nonce", responseNonce}
		container.Ports = []corev1.ContainerPort{{Name: "probe", ContainerPort: port, Protocol: corev1.ProtocolTCP}}
	} else {
		container.Command = []string{"/fixora-service-probe"}
		container.Args = []string{"--request-json", requestJSON}
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: cloneLabels(labelsMap), Annotations: annotations}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &falseValue, EnableServiceLinks: &falseValue, RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &trueValue, RunAsUser: &uid, RunAsGroup: &gid, SeccompProfile: seccomp}, Containers: []corev1.Container{container}, TerminationGracePeriodSeconds: ptrInt64(0), DNSPolicy: corev1.DNSDefault}}
}

func ptrInt64(value int64) *int64 { return &value }

func targetCaseLabels(c ConnectivityCase) (target, peer map[string]string) {
	if c.Direction == "ingress" {
		return c.DestinationPodLabels, c.SourcePodLabels
	}
	return c.SourcePodLabels, c.DestinationPodLabels
}
func targetNamespaceLabels(c ConnectivityCase) (target, peer map[string]string) {
	if c.Direction == "ingress" {
		return c.DestinationNamespaceLabels, c.SourceNamespaceLabels
	}
	return c.SourceNamespaceLabels, c.DestinationNamespaceLabels
}

func casePort(c ConnectivityCase) int32 { return c.Port }
func validSandboxAddress(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.PodIP == "" {
		return false
	}
	address, err := netip.ParseAddr(pod.Status.PodIP)
	return err == nil && !address.IsLoopback() && !address.IsUnspecified() && !address.IsMulticast() && address.Zone() == ""
}

func validateSandboxPolicy(want, got *networkingv1.NetworkPolicy, marker string) error {
	if got == nil || got.UID == "" || got.Name != want.Name || got.Namespace != want.Namespace || got.Annotations[runMarkerAnnotation] != marker || got.DeletionTimestamp != nil || !reflect.DeepEqual(got.Spec, want.Spec) {
		return errors.New("sandbox NetworkPolicy admission changed identity or approved spec")
	}
	return nil
}
