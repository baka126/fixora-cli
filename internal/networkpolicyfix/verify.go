package networkpolicyfix

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Runtime interface {
	Reader
	AdmitPolicy(context.Context, *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error)
	CreateNamespace(context.Context, *corev1.Namespace) (*corev1.Namespace, error)
	CreatePolicy(context.Context, *networkingv1.NetworkPolicy) (*networkingv1.NetworkPolicy, error)
	CreatePod(context.Context, *corev1.Pod) (*corev1.Pod, error)
	ReadNamespace(context.Context, string) (*corev1.Namespace, error)
	ReadPolicy(context.Context, string, string) (*networkingv1.NetworkPolicy, error)
	ReadPod(context.Context, string, string) (*corev1.Pod, error)
	FindAttempt(context.Context, string, string, string) ([]ObjectRef, error)
	PodLogs(context.Context, string, string, int64) ([]byte, error)
	DeleteOwned(context.Context, ObjectRef) error
}

type CaseOutcome struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
	Expected  string `json:"expected"`
	Observed  string `json:"observed"`
}

type Verification struct {
	Status   string            `json:"status"`
	Scope    string            `json:"scope"`
	Controls map[string]string `json:"controls"`
	Cases    []CaseOutcome     `json:"cases"`
	Cleanup  []CleanupStatus   `json:"cleanup,omitempty"`
	Receipt  *Receipt          `json:"-" yaml:"-"`
}

type CleanupStatus struct {
	ObjectRef
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type receiptState struct {
	mu       sync.Mutex
	consumed bool
}
type Receipt struct {
	proof, witness, binding string
	state                   *receiptState
}

func (r *Receipt) Check(p Plan) error {
	if r == nil || p.proof == nil || p.CheckBound() != nil || r.proof != p.proof.public || r.witness == "" || r.binding != hashJSON([]string{r.proof, r.witness}) || r.state == nil {
		return errors.New("matching fresh NetworkPolicy verification receipt required")
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.consumed {
		return errors.New("NetworkPolicy verification receipt already consumed")
	}
	return nil
}
func (r *Receipt) consume(p Plan) error {
	if err := r.Check(p); err != nil {
		return err
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.consumed {
		return errors.New("NetworkPolicy verification receipt already consumed")
	}
	r.state.consumed = true
	return nil
}

func Verify(parent context.Context, r Runtime, plan Plan) (out Verification, err error) {
	if r == nil {
		return out, errors.New("NetworkPolicy verifier runtime required")
	}
	ctx, cancel := operationContext(parent)
	defer cancel()
	out.Status = "verification-failed"
	if err = plan.CheckBound(); err != nil {
		return out, err
	}
	fresh, err := Prepare(ctx, r, plan.proof.authority, plan.Target)
	if err != nil {
		return out, errors.New("current policy, workload or Pod evidence changed before verification")
	}
	if fresh.proof.state != plan.proof.state {
		return out, errors.New("NetworkPolicy evidence changed before verification")
	}
	admitted, err := r.AdmitPolicy(ctx, plan.Candidate.DeepCopy())
	if err != nil || !samePolicyIntent(plan.Candidate, admitted) {
		return out, errors.New("server dry-run rejected or rewrote the approved NetworkPolicy spec")
	}
	marker, err := randomMarker()
	if err != nil {
		return out, errors.New("verification nonce unavailable")
	}
	owned := []ObjectRef{}
	pending := []*pendingObject{}
	var receipt *Receipt
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cleanupCancel()
		for _, item := range pending {
			if item.resolved {
				continue
			}
			refs, findErr := r.FindAttempt(cleanupCtx, item.ref.Namespace, item.ref.Name, item.ref.RunMarker)
			if findErr == nil && len(refs) == 1 && sameObjectRef(item.ref, refs[0]) {
				owned = append(owned, refs[0])
				item.resolved = true
				continue
			}
			out.Cleanup = append(out.Cleanup, CleanupStatus{ObjectRef: item.ref, Status: "cleanup-incomplete", Detail: "Creation outcome is unresolved; inspect only the exact run-owned resource"})
			out.Status = "cleanup-incomplete"
			out.Receipt = nil
			if err == nil {
				err = errors.New("NetworkPolicy verifier creation outcome unresolved")
			}
		}
		for i := len(owned) - 1; i >= 0; i-- {
			ref := owned[i]
			status := CleanupStatus{ObjectRef: ref, Status: "deleted"}
			if cleanupErr := r.DeleteOwned(cleanupCtx, ref); cleanupErr != nil {
				status.Status = "cleanup-incomplete"
				status.Detail = "UID-guarded cleanup failed; inspect the exact run-owned resource"
				out.Status = "cleanup-incomplete"
				out.Receipt = nil
				if err == nil {
					err = errors.New("NetworkPolicy verifier cleanup incomplete")
				}
			}
			out.Cleanup = append(out.Cleanup, status)
		}
		if err == nil && out.Status != "cleanup-incomplete" {
			out.Receipt = receipt
			out.Status = "verified-policy-contract"
		}
	}()
	allowCase := ConnectivityCase{}
	for _, c := range plan.proof.authority.Cases() {
		if c.Expected == "allow" {
			allowCase = c
			break
		}
	}
	if allowCase.Name == "" {
		return out, errors.New("approved CNI baseline case unavailable")
	}
	open, err := runScenario(ctx, r, plan, allowCase, marker, 1, "baseline-open", "none", &owned, &pending)
	if err != nil {
		return out, err
	}
	if !open {
		return out, errors.New("no-policy CNI control did not allow the declared flow")
	}
	out.Controls = map[string]string{"noPolicyAllow": "passed"}
	closed, err := runScenario(ctx, r, plan, allowCase, marker, 2, "deny-control", "deny", &owned, &pending)
	if err != nil {
		return out, err
	}
	if closed {
		return out, errors.New("default-deny CNI control did not block the declared flow")
	}
	out.Controls["defaultDeny"] = "passed"

	for i, c := range plan.proof.authority.Cases() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		observed, runErr := runScenario(ctx, r, plan, c, marker, i+3, c.Name, "candidate", &owned, &pending)
		if runErr != nil {
			return out, runErr
		}
		want := c.Expected == "allow"
		if observed != want {
			return out, fmt.Errorf("declared NetworkPolicy case %s did not match its approved outcome", c.Name)
		}
		value := "deny"
		if observed {
			value = "allow"
		}
		out.Cases = append(out.Cases, CaseOutcome{Name: c.Name, Direction: c.Direction, Expected: c.Expected, Observed: value})
	}
	fresh, err = Prepare(ctx, r, plan.proof.authority, plan.Target)
	if err != nil || fresh.proof.state != plan.proof.state {
		return out, errors.New("policy, workload or Pod evidence changed during isolated verification")
	}
	if err = plan.proof.authority.CheckUnchanged(); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	witness := hashJSON([]any{marker, out.Controls, out.Cases, plan.proof.state, plan.Candidate.Spec})
	receipt = &Receipt{proof: plan.proof.public, witness: witness, state: &receiptState{}}
	receipt.binding = hashJSON([]string{receipt.proof, receipt.witness})
	out.Scope = "declared TCP allow/deny cases with CNI enforcement controls"
	return out, nil
}

func runScenario(ctx context.Context, r Runtime, plan Plan, c ConnectivityCase, marker string, index int, stage, mode string, owned *[]ObjectRef, pending *[]*pendingObject) (bool, error) {
	srcNS, dstNS := scenarioNamespaces(marker, index, c.SourceNamespaceLabels, c.DestinationNamespaceLabels)
	createdSrc, err := createNamespace(ctx, r, srcNS, owned, pending)
	if err != nil {
		return false, err
	}
	createdDst := (*corev1.Namespace)(nil)
	if caseNeedsSameNamespace(c, plan.proof.authority.NetworkPolicySpec()) {
		if !reflect.DeepEqual(c.SourceNamespaceLabels, c.DestinationNamespaceLabels) {
			return false, errors.New("podSelector-only peer requires equal source and destination namespace labels")
		}
		createdDst = createdSrc
	} else {
		createdDst, err = createNamespace(ctx, r, dstNS, owned, pending)
		if err != nil {
			return false, err
		}
	}
	if err = checkNamespaceLabels(plan.proof.authority, c.SourceNamespaceLabels, createdSrc); err != nil {
		return false, err
	}
	if err = checkNamespaceLabels(plan.proof.authority, c.DestinationNamespaceLabels, createdDst); err != nil {
		return false, err
	}
	targetNs, peerNs := createdDst, createdSrc
	targetLabels, peerLabels := c.DestinationPodLabels, c.SourcePodLabels
	if c.Direction == "egress" {
		targetNs, peerNs = createdSrc, createdDst
		targetLabels, peerLabels = c.SourcePodLabels, c.DestinationPodLabels
	}
	if mode != "none" {
		name := objectName(marker, index, "policy")
		var spec networkingv1.NetworkPolicySpec
		if mode == "deny" {
			spec = networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: cloneLabels(targetLabels)}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}}
		} else {
			spec = plan.Candidate.Spec
		}
		want := sandboxPolicy(targetNs.Name, name, marker, stage, spec)
		got, createErr := createPolicy(ctx, r, want, owned, pending)
		if createErr != nil {
			return false, createErr
		}
		if err = validateSandboxPolicy(want, got, marker); err != nil {
			return false, err
		}
		fresh, readErr := r.ReadPolicy(ctx, targetNs.Name, name)
		if readErr != nil || validateSandboxPolicy(want, fresh, marker) != nil {
			return false, errors.New("sandbox policy readback did not match admitted spec")
		}
	}
	serverNs, clientNs := targetNs, peerNs
	serverLabels, clientLabels := targetLabels, peerLabels
	if c.Direction == "egress" {
		serverNs, clientNs = peerNs, targetNs
		serverLabels, clientLabels = peerLabels, targetLabels
	}
	responseNonce, err := randomMarker()
	if err != nil {
		return false, errors.New("probe response nonce unavailable")
	}
	port := casePort(c)
	serverWant := probePod(serverNs.Name, objectName(marker, index, "server"), marker, "server", stage, plan.proof.authority.ProbeImage(), serverLabels, port, responseNonce, plan.proof.authority.Timeout(), "")
	server, err := createPod(ctx, r, serverWant, owned, pending)
	if err != nil {
		return false, err
	}
	if err = checkSandboxPod(serverWant, server, marker); err != nil {
		return false, err
	}
	server, err = waitServer(ctx, r, serverWant, server)
	if err != nil {
		return false, err
	}
	first := -1
	for attempt := 0; attempt < 2; attempt++ {
		requestNonce, nonceErr := randomMarker()
		if nonceErr != nil {
			return false, errors.New("probe request nonce unavailable")
		}
		req := serviceprobe.Request{Nonce: requestNonce, Targets: []serviceprobe.Target{{Address: server.Status.PodIP, Port: int(port), PodUID: string(server.UID)}}, Contract: serviceprobe.Contract{Method: "tcp", Timeout: plan.proof.authority.Timeout(), ExpectedResponseNonce: responseNonce}}
		if err = serviceprobe.Validate(req); err != nil {
			return false, errors.New("bounded TCP probe request invalid")
		}
		raw, _ := json.Marshal(req)
		// Keep the retry ordinal at the end of the name. Truncating the scenario
		// name here could collapse attempt 0 and attempt 1 to the same object.
		clientWant := probePod(clientNs.Name, objectName(marker, index, fmt.Sprintf("client-%d", attempt)), marker, "client", stage, plan.proof.authority.ProbeImage(), clientLabels, port, "", plan.proof.authority.Timeout(), string(raw))
		client, createErr := createPod(ctx, r, clientWant, owned, pending)
		if createErr != nil {
			return false, createErr
		}
		if err = checkSandboxPod(clientWant, client, marker); err != nil {
			return false, err
		}
		client, err = waitClient(ctx, r, clientWant, client)
		if err != nil {
			return false, err
		}
		observed, reportErr := probeOutcome(ctx, r, client, req)
		if reportErr != nil {
			return false, reportErr
		}
		if observed != (client.Status.Phase == corev1.PodSucceeded) {
			return false, errors.New("probe process status disagrees with nonce-bound result")
		}
		value := 0
		if observed {
			value = 1
		}
		if attempt == 0 {
			first = value
			continue
		}
		if first != value {
			return false, errors.New("repeated TCP observations disagreed")
		}
		return observed, nil
	}
	return false, errors.New("repeated connectivity outcome unavailable")
}

func caseNeedsSameNamespace(c ConnectivityCase, spec networkingv1.NetworkPolicySpec) bool {
	var rules []networkingv1.NetworkPolicyIngressRule
	var egress []networkingv1.NetworkPolicyEgressRule
	var podLabels map[string]string
	if c.Direction == "ingress" {
		rules = spec.Ingress
		podLabels = c.SourcePodLabels
	} else {
		egress = spec.Egress
		podLabels = c.DestinationPodLabels
	}
	if c.Direction == "ingress" {
		for _, rule := range rules {
			for _, port := range rule.Ports {
				if port.Port == nil || port.Port.IntVal != c.Port {
					continue
				}
				for _, peer := range rule.From {
					if peer.PodSelector != nil && peer.NamespaceSelector == nil && selectorMatches(peer.PodSelector, podLabels) {
						return true
					}
				}
			}
		}
	} else {
		for _, rule := range egress {
			for _, port := range rule.Ports {
				if port.Port == nil || port.Port.IntVal != c.Port {
					continue
				}
				for _, peer := range rule.To {
					if peer.PodSelector != nil && peer.NamespaceSelector == nil && selectorMatches(peer.PodSelector, podLabels) {
						return true
					}
				}
			}
		}
	}
	return false
}

func createNamespace(ctx context.Context, r Runtime, want *corev1.Namespace, owned *[]ObjectRef, pending *[]*pendingObject) (*corev1.Namespace, error) {
	item := &pendingObject{ref: ObjectRef{Kind: "Namespace", Name: want.Name, RunMarker: want.Annotations[runMarkerAnnotation]}}
	*pending = append(*pending, item)
	got, err := r.CreateNamespace(ctx, want.DeepCopy())
	if err != nil {
		return nil, errors.New("disposable namespace creation failed; outcome may be partial")
	}
	if got == nil || got.Name != want.Name || got.UID == "" || got.Annotations[runMarkerAnnotation] != item.ref.RunMarker {
		return nil, errors.New("disposable namespace identity or run marker changed")
	}
	item.ref.UID = string(got.UID)
	item.resolved = true
	*owned = append(*owned, item.ref)
	fresh, readErr := r.ReadNamespace(ctx, want.Name)
	if readErr != nil || fresh == nil || fresh.UID != got.UID || fresh.ResourceVersion == "" || fresh.Annotations[runMarkerAnnotation] != item.ref.RunMarker {
		return nil, errors.New("disposable namespace readback incomplete")
	}
	return fresh, nil
}

func createPolicy(ctx context.Context, r Runtime, want *networkingv1.NetworkPolicy, owned *[]ObjectRef, pending *[]*pendingObject) (*networkingv1.NetworkPolicy, error) {
	item := &pendingObject{ref: ObjectRef{Kind: "NetworkPolicy", Namespace: want.Namespace, Name: want.Name, RunMarker: want.Annotations[runMarkerAnnotation]}}
	*pending = append(*pending, item)
	got, err := r.CreatePolicy(ctx, want.DeepCopy())
	if err != nil {
		return nil, errors.New("disposable NetworkPolicy creation failed; outcome may be partial")
	}
	if got == nil || got.UID == "" || got.Name != want.Name || got.Namespace != want.Namespace || got.Annotations[runMarkerAnnotation] != item.ref.RunMarker {
		return nil, errors.New("disposable NetworkPolicy identity or run marker changed")
	}
	item.ref.UID = string(got.UID)
	item.resolved = true
	*owned = append(*owned, item.ref)
	return got, nil
}

func createPod(ctx context.Context, r Runtime, want *corev1.Pod, owned *[]ObjectRef, pending *[]*pendingObject) (*corev1.Pod, error) {
	item := &pendingObject{ref: ObjectRef{Kind: "Pod", Namespace: want.Namespace, Name: want.Name, RunMarker: want.Annotations[runMarkerAnnotation]}}
	*pending = append(*pending, item)
	got, err := r.CreatePod(ctx, want.DeepCopy())
	if err != nil {
		return nil, errors.New("disposable Pod creation failed; outcome may be partial")
	}
	if got == nil || got.UID == "" || got.Name != want.Name || got.Namespace != want.Namespace || got.Annotations[runMarkerAnnotation] != item.ref.RunMarker {
		return nil, errors.New("disposable Pod identity or run marker changed")
	}
	item.ref.UID = string(got.UID)
	item.resolved = true
	*owned = append(*owned, item.ref)
	return got, nil
}

func checkNamespaceLabels(a *Authority, expected map[string]string, actual *corev1.Namespace) error {
	if actual == nil || actual.UID == "" || actual.ResourceVersion == "" {
		return errors.New("disposable namespace identity unavailable")
	}
	for key, value := range expected {
		if actual.Labels[key] != value {
			return errors.New("namespace admission changed an approved case label")
		}
	}
	spec := a.NetworkPolicySpec()
	for _, selector := range namespaceSelectors(spec) {
		if selector.Matches(labels.Set(expected)) != selector.Matches(labels.Set(actual.Labels)) {
			return errors.New("namespace admission changed NetworkPolicy selector meaning")
		}
	}
	return nil
}

func namespaceSelectors(spec networkingv1.NetworkPolicySpec) []labels.Selector {
	out := []labels.Selector{}
	add := func(peer networkingv1.NetworkPolicyPeer) {
		if peer.NamespaceSelector != nil {
			if selector, err := metav1.LabelSelectorAsSelector(peer.NamespaceSelector); err == nil {
				out = append(out, selector)
			}
		}
	}
	for _, rule := range spec.Ingress {
		for _, p := range rule.From {
			add(p)
		}
	}
	for _, rule := range spec.Egress {
		for _, p := range rule.To {
			add(p)
		}
	}
	return out
}

func checkSandboxPod(want, got *corev1.Pod, marker string) error {
	if got == nil || got.UID == "" || got.Name != want.Name || got.Namespace != want.Namespace || got.Annotations[runMarkerAnnotation] != marker || !reflect.DeepEqual(got.Labels, want.Labels) || got.DeletionTimestamp != nil || got.Spec.HostNetwork || got.Spec.HostPID || got.Spec.HostIPC || got.Spec.ServiceAccountName != "" || len(got.Spec.HostAliases) != 0 || len(got.Spec.Volumes) != 0 || len(got.Spec.ImagePullSecrets) != 0 || got.Spec.AutomountServiceAccountToken == nil || *got.Spec.AutomountServiceAccountToken || !reflect.DeepEqual(got.Spec.SecurityContext, want.Spec.SecurityContext) || len(got.Spec.Containers) != 1 || len(got.Spec.InitContainers) != 0 || len(got.Spec.EphemeralContainers) != 0 {
		return errors.New("disposable probe Pod admission changed isolation or identity")
	}
	wantContainer, gotContainer := want.Spec.Containers[0], got.Spec.Containers[0]
	if wantContainer.Image != gotContainer.Image || !reflect.DeepEqual(wantContainer.Command, gotContainer.Command) || !reflect.DeepEqual(wantContainer.Args, gotContainer.Args) || !reflect.DeepEqual(wantContainer.SecurityContext, gotContainer.SecurityContext) || len(gotContainer.Env) != 0 || len(gotContainer.EnvFrom) != 0 || len(gotContainer.VolumeMounts) != 0 || len(gotContainer.VolumeDevices) != 0 {
		return errors.New("disposable probe command, image or security settings changed")
	}
	return nil
}

func waitServer(ctx context.Context, r Runtime, want, created *corev1.Pod) (*corev1.Pod, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pod, err := r.ReadPod(ctx, want.Namespace, created.Name)
		if err == nil && pod != nil && pod.UID == created.UID && pod.Status.Phase == corev1.PodRunning && validSandboxAddress(pod) {
			return pod, nil
		}
		if err = pause(ctx); err != nil {
			return nil, errors.New("TCP responder Pod did not become ready")
		}
	}
}

func waitClient(ctx context.Context, r Runtime, want, created *corev1.Pod) (*corev1.Pod, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pod, err := r.ReadPod(ctx, want.Namespace, created.Name)
		if err == nil && pod != nil && pod.UID == created.UID && (pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed) {
			return pod, nil
		}
		if err = pause(ctx); err != nil {
			return nil, errors.New("TCP client Pod did not finish within its deadline")
		}
	}
}

func probeOutcome(ctx context.Context, r Runtime, pod *corev1.Pod, request serviceprobe.Request) (bool, error) {
	raw, err := r.PodLogs(ctx, pod.Namespace, pod.Name, serviceprobe.MaxWireBytes)
	if err != nil || len(raw) > serviceprobe.MaxWireBytes {
		return false, errors.New("bounded TCP probe result unavailable")
	}
	line := bytes.SplitN(raw, []byte("\n"), 2)[0]
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var result serviceprobe.Result
	if dec.Decode(&result) != nil || result.Nonce != request.Nonce || len(result.Checks) != 1 {
		return false, errors.New("TCP probe result did not match its nonce-bound request")
	}
	check := result.Checks[0]
	target := request.Targets[0]
	if check.Address != target.Address || check.Port != target.Port || check.PodUID != target.PodUID {
		return false, errors.New("TCP probe witness did not identify the exact test endpoint")
	}
	if pod.Status.Phase == corev1.PodSucceeded && !check.Success || pod.Status.Phase == corev1.PodFailed && check.Success {
		return false, errors.New("TCP probe report disagrees with process exit status")
	}
	return check.Success, nil
}

func operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 3*time.Minute)
}
func pause(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func samePolicyIntent(want, got *networkingv1.NetworkPolicy) bool {
	return got != nil && want != nil && got.UID == want.UID && got.Namespace == want.Namespace && got.Name == want.Name && reflect.DeepEqual(got.Labels, want.Labels) && reflect.DeepEqual(got.Annotations, want.Annotations) && reflect.DeepEqual(got.Spec, want.Spec)
}
func sameObjectRef(want, got ObjectRef) bool {
	return want.Kind == got.Kind && want.Namespace == got.Namespace && want.Name == got.Name && want.RunMarker == got.RunMarker && got.UID != ""
}
func hashJSON(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
