package servicefix

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

type CleanupResult struct {
	ObjectRef
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type Verification struct {
	Status   string          `json:"status"`
	Scope    string          `json:"scope"`
	Summary  string          `json:"summary,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Cleanup  []CleanupResult `json:"cleanup,omitempty"`
	Receipt  *Receipt        `json:"-" yaml:"-"`
}
type Receipt struct{ proof, witness, binding string }

func (r *Receipt) Check(p *Proof) error {
	if r == nil || p == nil || p.binding == "" || p.binding != p.contentBinding() || r.proof != p.binding || r.witness == "" || r.binding != digest([]string{r.proof, r.witness}) {
		return errors.New("matching fresh Service verification receipt required")
	}
	return nil
}
func Verify(ctx context.Context, r Runtime, plan Plan) (out Verification, err error) {
	return verifySession(ctx, r, plan, nil, nil)
}
func verifySession(ctx context.Context, r Runtime, plan Plan, live *corev1.Service, fresh func(context.Context) error) (out Verification, err error) {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	out.Status = "verification-failed"
	p := plan.Proof
	if r == nil || p == nil {
		return out, errors.New("Service runtime and proof required")
	}
	if err = p.checkPlan(plan); err != nil {
		return out, err
	}
	if fresh == nil {
		fresh = func(c context.Context) error { return p.CheckFresh(c, r) }
	}
	if err = fresh(ctx); err != nil {
		return out, err
	}

	var e error
	if live == nil {
		admitted, e := r.Admit(ctx, p.candidate.DeepCopy())
		if e != nil || !sameServiceIntent(p.candidate, admitted) {
			return out, errors.New("Service admission validation failed or rewrote the candidate")
		}
	}
	nonceRaw := make([]byte, 16)
	if _, e = rand.Read(nonceRaw); e != nil {
		return out, e
	}
	nonce := hex.EncodeToString(nonceRaw)
	owned := []ObjectRef{}
	pending := []pendingCreate{}
	record := func(kind, ns, name, uid string) bool {
		if uid != "" && name != "" && ns == p.candidate.Namespace {
			owned = append(owned, ObjectRef{kind, ns, name, uid})
			return true
		}
		return false
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		for _, unknown := range pending {
			recovered, incomplete := recoverCreate(cleanupCtx, r, unknown, nonce)
			if recovered != nil {
				owned = append(owned, *recovered)
			} else {
				out.Cleanup = append(out.Cleanup, incomplete)
				out.Receipt = nil
				out.Status = "cleanup-incomplete"
				if err == nil {
					err = errors.New("verifier creation outcome unresolved")
				}
			}
		}
		for i := len(owned) - 1; i >= 0; i-- {
			ref := owned[i]
			result := CleanupResult{ObjectRef: ref, Status: "deleted"}
			if e := r.DeleteOwned(cleanupCtx, ref); e != nil {
				result.Status = "cleanup-incomplete"
				result.Detail = "UID-guarded cleanup failed; inspect this owned resource"
				out.Receipt = nil
				out.Status = "cleanup-incomplete"
				if err == nil {
					err = errors.New("Service verification cleanup incomplete")
				}
			}
			out.Cleanup = append(out.Cleanup, result)
		}
	}()
	svc := live
	if svc == nil {
		expected := temporaryService(p, nonce)
		svc, e = r.CreateService(ctx, expected)
		recorded := false
		if svc != nil {
			recorded = record("Service", svc.Namespace, svc.Name, string(svc.UID))
		}
		if !recorded {
			pending = append(pending, pendingCreate{kind: "Service", service: expected.DeepCopy()})
		}
		if e != nil {
			return out, errors.New("temporary Service creation failed; creation outcome may be partial")
		}
		if e = checkTemporary(expected, svc); e != nil {
			return out, e
		}
		if e = readTemporary(ctx, r, svc); e != nil {
			return out, e
		}
	}
	targets, e := waitEndpoints(ctx, r, svc, p.before)
	if e != nil {
		return out, e
	}
	req, e := makeProbeRequest(p, svc, targets, nonce)
	if e != nil {
		return out, e
	}
	pod, network, e := probeObjects(p, req)
	if e != nil {
		return out, e
	}
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int64(time.Until(deadline).Seconds())
		if seconds < 1 {
			return out, errors.New("verification deadline exhausted")
		}
		pod.Spec.ActiveDeadlineSeconds = &seconds
	}
	createdNet, e := r.CreatePolicy(ctx, network)
	recordedNet := false
	if createdNet != nil {
		recordedNet = record("NetworkPolicy", createdNet.Namespace, createdNet.Name, string(createdNet.UID))
	}
	if !recordedNet {
		pending = append(pending, pendingCreate{kind: "NetworkPolicy", policy: network.DeepCopy()})
	}
	if e != nil || createdNet == nil || createdNet.UID == "" || createdNet.Namespace != network.Namespace || !reflect.DeepEqual(createdNet.Spec, network.Spec) {
		return out, errors.New("verifier isolation policy failed admission")
	}
	if e = readIsolation(ctx, r, createdNet); e != nil {
		return out, e
	}
	createdPod, e := r.CreatePod(ctx, pod)
	recordedPod := false
	if createdPod != nil {
		recordedPod = record("Pod", createdPod.Namespace, createdPod.Name, string(createdPod.UID))
	}
	if !recordedPod {
		pending = append(pending, pendingCreate{kind: "Pod", pod: pod.DeepCopy()})
	}
	if e != nil {
		return out, errors.New("verifier creation failed; creation outcome may be partial")
	}
	if e = checkProbe(pod, createdPod); e != nil {
		return out, e
	}
	finished, e := waitProbe(ctx, r, pod, createdPod)
	if e != nil {
		return out, e
	}
	raw, e := r.PodLogs(ctx, finished.Namespace, finished.Name, serviceprobe.MaxWireBytes)
	if e != nil {
		return out, errors.New("complete verifier report unavailable")
	}
	if e = checkProbeWitness(ctx, r, pod, finished); e != nil {
		return out, e
	}
	result, e := readProbeResult(raw, req)
	if e != nil {
		return out, e
	}
	if e = readIsolation(ctx, r, createdNet); e != nil {
		return out, e
	}
	if e = fresh(ctx); e != nil {
		return out, e
	}
	slices, e := r.ListSlices(ctx, svc.Namespace, svc.Name)
	if e != nil {
		return out, errors.New("temporary Service endpoints unreadable at certification")
	}
	if _, e = CheckEndpoints(svc, slices, p.before); e != nil {
		return out, e
	}
	if e = readTemporary(ctx, r, svc); e != nil {
		return out, e
	}
	receipt := &Receipt{proof: p.binding, witness: digest([]any{nonce, svc.UID, createdPod.UID, finished.Status.ContainerStatuses, result})}
	receipt.binding = digest([]string{receipt.proof, receipt.witness})
	out.Receipt = receipt
	out.Status = "verification-passed"
	out.Scope = p.authority.intent.Spec.Verification.Method + "-connectivity-contract"
	out.Summary = "Every captured backend address and selected Service IP matched the approved contract"
	out.Warnings = []string{"This verifies the selected transport/HTTP contract, not dependent application recovery or CNI packet enforcement"}
	return out, nil
}
func waitEndpoints(ctx context.Context, r Runtime, svc *corev1.Service, s Snapshot) ([]serviceprobe.Target, error) {
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		slices, err := r.ListSlices(ctx, svc.Namespace, svc.Name)
		if err != nil {
			return nil, errors.New("EndpointSlice verification inventory unreadable")
		}
		if len(slices) > 0 {
			targets, checkErr := CheckEndpoints(svc, slices, s)
			if checkErr == nil {
				return targets, nil
			}
			last = checkErr
		}
		if err = pause(ctx); err != nil {
			if last != nil {
				return nil, fmt.Errorf("EndpointSlice convergence failed (%v): %w", last, err)
			}
			return nil, err
		}
	}
}
func pause(ctx context.Context) error {
	t := time.NewTimer(100 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func waitProbe(ctx context.Context, r Runtime, expected, created *corev1.Pod) (*corev1.Pod, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := r.ReadPod(ctx, created.Namespace, created.Name)
		if err != nil {
			return nil, errors.New("verifier Pod unreadable")
		}
		if p == nil || p.UID != created.UID {
			return nil, errors.New("verifier was replaced")
		}
		if err = checkProbe(expected, p); err != nil {
			return nil, err
		}
		if p.Status.Phase == corev1.PodFailed {
			return nil, errors.New("verifier Pod failed")
		}
		if p.Status.Phase == corev1.PodSucceeded {
			if len(p.Status.ContainerStatuses) != 1 {
				return nil, errors.New("verifier termination evidence missing")
			}
			c := p.Status.ContainerStatuses[0]
			if c.Name != "verifier" || c.Image != expected.Spec.Containers[0].Image || c.ImageID == "" || c.State.Terminated == nil || c.State.Terminated.ExitCode != 0 || c.RestartCount != 0 {
				return nil, errors.New("verifier binary did not terminate successfully")
			}
			return p, nil
		}
		if err = pause(ctx); err != nil {
			return nil, err
		}
	}
}
func readProbeResult(raw []byte, req serviceprobe.Request) (serviceprobe.Result, error) {
	var result serviceprobe.Result
	if len(raw) > serviceprobe.MaxWireBytes {
		return result, errors.New("probe report exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil {
		return result, errors.New("invalid probe report")
	}
	var extra any
	if d.Decode(&extra) != io.EOF || result.Nonce != req.Nonce || len(result.Checks) != len(req.Targets) {
		return result, errors.New("probe report does not match verification attempt")
	}
	expected := map[serviceprobe.Target]bool{}
	for _, t := range req.Targets {
		expected[t] = true
	}
	for _, c := range result.Checks {
		if !expected[c.Target] || !c.Success || c.Error != "" || req.Contract.Method == "http" && c.Status != req.Contract.ExpectedStatus || req.Contract.Method == "tcp" && c.Status != 0 {
			return result, errors.New("not every requested destination passed")
		}
		delete(expected, c.Target)
	}
	if len(expected) != 0 {
		return result, errors.New("backend verification report is incomplete")
	}
	return result, nil
}

func readTemporary(ctx context.Context, r Runtime, expected *corev1.Service) error {
	got, err := r.GetService(ctx, expected.Namespace, expected.Name)
	if err != nil || got == nil || got.UID != expected.UID || got.Name != expected.Name || got.Namespace != expected.Namespace || got.DeletionTimestamp != nil || !reflect.DeepEqual(got.Spec, expected.Spec) || !reflect.DeepEqual(got.Labels, expected.Labels) || !reflect.DeepEqual(got.Annotations, expected.Annotations) || !reflect.DeepEqual(got.OwnerReferences, expected.OwnerReferences) {
		return errors.New("temporary Service routing or identity changed")
	}
	return nil
}

func readIsolation(ctx context.Context, r Runtime, expected *networkingv1.NetworkPolicy) error {
	got, err := r.ReadPolicy(ctx, expected.Namespace, expected.Name)
	if err != nil || got == nil || got.UID != expected.UID || got.Name != expected.Name || got.Namespace != expected.Namespace || got.DeletionTimestamp != nil || !reflect.DeepEqual(got.Spec, expected.Spec) {
		return errors.New("verifier isolation policy changed or is unreadable")
	}
	return nil
}

func checkProbeWitness(ctx context.Context, r Runtime, expected, finished *corev1.Pod) error {
	got, err := r.ReadPod(ctx, finished.Namespace, finished.Name)
	if err != nil || got == nil || got.UID != finished.UID || got.Name != finished.Name || got.Namespace != finished.Namespace || got.Status.Phase != corev1.PodSucceeded {
		return errors.New("verifier identity changed after report retrieval")
	}
	if err = checkProbe(expected, got); err != nil {
		return err
	}
	if !reflect.DeepEqual(got.Status.ContainerStatuses, finished.Status.ContainerStatuses) {
		return errors.New("verifier termination evidence changed after report retrieval")
	}
	return nil
}
