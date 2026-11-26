package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
)

type helmServicePortID struct{ Name, Protocol, Port string }

func expectedHelmServiceTarget(base map[string]any, patch string) (map[string]any, error) {
	var n yaml.Node
	if err := decodeHelmValues([]byte(patch), &n); err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := n.Decode(&obj); err != nil {
		return nil, err
	}
	meta, _ := obj["metadata"].(map[string]any)
	bm, _ := base["metadata"].(map[string]any)
	if obj["apiVersion"] != "v1" || obj["kind"] != "Service" || meta["name"] != bm["name"] || meta["namespace"] != bm["namespace"] || len(meta) != 2 {
		return nil, errors.New("candidate Service identity differs")
	}
	for field := range obj {
		if field != "apiVersion" && field != "kind" && field != "metadata" && field != "spec" {
			return nil, errors.New("candidate Service contains unsupported fields")
		}
	}
	spec, _ := obj["spec"].(map[string]any)
	if len(spec) == 0 {
		return nil, errors.New("candidate Service has no routing fields")
	}
	for field := range spec {
		if field != "selector" && field != "ports" {
			return nil, errors.New("candidate Service contains unsupported routing fields")
		}
	}
	if selector, ok := spec["selector"]; ok {
		m, valid := selector.(map[string]any)
		if !valid || m["$patch"] != "replace" || len(m) < 2 {
			return nil, errors.New("Service selector must be an explicit replacement")
		}
	}
	if ports, ok := spec["ports"]; ok {
		list, valid := ports.([]any)
		if !valid || len(list) != 1 {
			return nil, errors.New("Service candidate must update exactly one selected port")
		}
		port, valid := list[0].(map[string]any)
		if !valid {
			return nil, errors.New("invalid selected Service port")
		}
		for field := range port {
			if field != "name" && field != "port" && field != "protocol" && field != "targetPort" {
				return nil, errors.New("selected Service port contains unsupported fields")
			}
		}
		if _, ok := port["targetPort"]; !ok {
			return nil, errors.New("selected Service port must set targetPort")
		}
	}
	if err := validateHelmService(base); err != nil {
		return nil, err
	}
	br, _ := json.Marshal(base)
	pr, _ := json.Marshal(obj)
	data, err := strategicpatch.StrategicMergePatch(br, pr, corev1.Service{})
	if err != nil {
		return nil, errors.New("Service routing patch cannot be merged")
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if err := validateHelmServiceRoutingChange(base, out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateHelmService(obj map[string]any) error {
	if obj["apiVersion"] != "v1" || obj["kind"] != "Service" {
		return errors.New("only core/v1 Services are supported")
	}
	spec, _ := obj["spec"].(map[string]any)
	serviceType := stringField(spec, "type")
	if serviceType != "" && serviceType != string(corev1.ServiceTypeClusterIP) || stringField(spec, "clusterIP") == corev1.ClusterIPNone {
		return errors.New("only ClusterIP Services are supported")
	}
	selector, _ := spec["selector"].(map[string]any)
	if len(selector) == 0 {
		return errors.New("selectorless Services are unsupported")
	}
	ports, _ := spec["ports"].([]any)
	if len(ports) == 0 {
		return errors.New("Service has no ports")
	}
	ids := map[helmServicePortID]bool{}
	mergeKeys := map[string]bool{}
	for _, raw := range ports {
		port, ok := raw.(map[string]any)
		if !ok {
			return errors.New("invalid Service port")
		}
		protocol := stringField(port, "protocol")
		if protocol == "" {
			protocol = string(corev1.ProtocolTCP)
		}
		if protocol != string(corev1.ProtocolTCP) {
			return errors.New("only TCP Service ports are supported")
		}
		id, err := helmServicePortIdentity(port)
		mergeKey := fmt.Sprint(port["port"])
		if err != nil || ids[id] || mergeKeys[mergeKey] {
			return errors.New("Service port identity is missing or ambiguous")
		}
		ids[id] = true
		mergeKeys[mergeKey] = true
	}
	return nil
}

func helmServicePortIdentity(port map[string]any) (helmServicePortID, error) {
	protocol := stringField(port, "protocol")
	if protocol == "" {
		protocol = string(corev1.ProtocolTCP)
	}
	portNumber, ok := port["port"]
	if !ok {
		return helmServicePortID{}, errors.New("Service port number required")
	}
	name := stringField(port, "name")
	if name != "" {
		return helmServicePortID{name, protocol, fmt.Sprint(portNumber)}, nil
	}
	return helmServicePortID{"", protocol, fmt.Sprint(portNumber)}, nil
}

func validateHelmServicePortOrder(before, after map[string]any) error {
	bs, _ := before["spec"].(map[string]any)
	as, _ := after["spec"].(map[string]any)
	bp, _ := bs["ports"].([]any)
	ap, _ := as["ports"].([]any)
	if len(bp) == 0 || len(bp) != len(ap) {
		return errors.New("Service port list changed")
	}
	seen := map[helmServicePortID]bool{}
	for i := range bp {
		b, _ := bp[i].(map[string]any)
		a, _ := ap[i].(map[string]any)
		bid, be := helmServicePortIdentity(b)
		aid, ae := helmServicePortIdentity(a)
		if be != nil || ae != nil || seen[bid] || bid != aid {
			return errors.New("Service port identity is ambiguous, changed, or reordered")
		}
		seen[bid] = true
	}
	return nil
}

func validateHelmServiceRoutingChange(before, after map[string]any) error {
	if err := validateHelmService(before); err != nil {
		return err
	}
	if err := validateHelmService(after); err != nil {
		return err
	}
	if err := validateHelmServicePortOrder(before, after); err != nil {
		return err
	}
	bClean := copyHelmObject(before)
	aClean := copyHelmObject(after)
	bSpec, _ := bClean["spec"].(map[string]any)
	aSpec, _ := aClean["spec"].(map[string]any)
	delete(bSpec, "selector")
	delete(aSpec, "selector")
	stripServiceTargetPorts(bSpec)
	stripServiceTargetPorts(aSpec)
	normalizeHelmServiceDefaults(bClean)
	normalizeHelmServiceDefaults(aClean)
	if !reflect.DeepEqual(semanticHelm(bClean, ""), semanticHelm(aClean, "")) {
		return errors.New("rendered Service changes fields outside selector and targetPort")
	}
	return nil
}

func helmServiceSemantic(obj map[string]any) any {
	normalized := copyHelmObject(obj)
	normalizeHelmServiceDefaults(normalized)
	return semanticHelm(normalized, "")
}

func normalizeHelmServiceDefaults(obj map[string]any) {
	spec, _ := obj["spec"].(map[string]any)
	if stringField(spec, "type") == string(corev1.ServiceTypeClusterIP) {
		delete(spec, "type")
	}
	if stringField(spec, "sessionAffinity") == string(corev1.ServiceAffinityNone) {
		delete(spec, "sessionAffinity")
	}
	if stringField(spec, "internalTrafficPolicy") == string(corev1.ServiceInternalTrafficPolicyCluster) {
		delete(spec, "internalTrafficPolicy")
	}
	ports, _ := spec["ports"].([]any)
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		if stringField(port, "name") == "" {
			delete(port, "name")
		}
		if protocol := stringField(port, "protocol"); protocol == "" || protocol == string(corev1.ProtocolTCP) {
			delete(port, "protocol")
		}
		if target, exists := port["targetPort"]; exists && sameServicePortNumber(target, port["port"]) {
			delete(port, "targetPort")
		}
	}
}

func sameServicePortNumber(a, b any) bool {
	_, aString := a.(string)
	_, bString := b.(string)
	return !aString && !bString && reflect.DeepEqual(fmt.Sprint(a), fmt.Sprint(b))
}

func helmServiceAdmissionMatches(expected, admitted, live map[string]any) bool {
	meta := mapAt(expected, "metadata")
	key := ResourceKey{"v1", "Service", stringField(meta, "namespace"), stringField(meta, "name")}
	if !sameHelmTargetIdentity(admitted, key) || !helmAdmittedPreserves(expected, admitted) {
		return false
	}
	expectedCopy := copyHelmObject(expected)
	expectedSpec := mapAt(expectedCopy, "spec")
	admittedCopy := copyHelmObject(admitted)
	admittedSpec, _ := admittedCopy["spec"].(map[string]any)
	liveSpec := mapAt(live, "spec")
	for _, field := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
		if _, supplied := expectedSpec[field]; !supplied {
			if admittedValue, added := admittedSpec[field]; added {
				liveValue, existedLive := liveSpec[field]
				if !existedLive || !reflect.DeepEqual(admittedValue, liveValue) {
					return false
				}
			}
			delete(expectedSpec, field)
			delete(admittedSpec, field)
		}
	}
	normalizeHelmServiceDefaults(expectedCopy)
	normalizeHelmServiceDefaults(admittedCopy)
	expectedSpec = mapAt(expectedCopy, "spec")
	admittedSpec = mapAt(admittedCopy, "spec")
	return reflect.DeepEqual(semanticHelm(expectedSpec, ""), semanticHelm(admittedSpec, ""))
}

func mapAt(obj map[string]any, key string) map[string]any {
	value, _ := obj[key].(map[string]any)
	return value
}

func helmServiceRoutingProjection(obj map[string]any) map[string]any {
	spec, _ := obj["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	routes := make([]any, 0, len(ports))
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		id, _ := helmServicePortIdentity(port)
		target, exists := port["targetPort"]
		if !exists {
			target = port["port"]
		}
		routes = append(routes, map[string]any{"identity": id, "targetPort": target})
	}
	serviceType := stringField(spec, "type")
	if serviceType == "" {
		serviceType = string(corev1.ServiceTypeClusterIP)
	}
	return map[string]any{"type": serviceType, "selector": spec["selector"], "ports": routes}
}

func stripServiceTargetPorts(spec map[string]any) {
	ports, _ := spec["ports"].([]any)
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		delete(port, "targetPort")
	}
}

func validateHelmServiceMappings(base, expected map[string]any, mappings []HelmMapping) error {
	bs, _ := base["spec"].(map[string]any)
	es, _ := expected["spec"].(map[string]any)
	need := map[string]bool{}
	if !reflect.DeepEqual(bs["selector"], es["selector"]) {
		need["/spec/selector"] = true
	}
	if err := validateHelmServicePortOrder(base, expected); err != nil {
		return err
	}
	bp, _ := bs["ports"].([]any)
	ep, _ := es["ports"].([]any)
	for i := range bp {
		b, _ := bp[i].(map[string]any)
		e, _ := ep[i].(map[string]any)
		if !reflect.DeepEqual(b["targetPort"], e["targetPort"]) {
			need[fmt.Sprintf("/spec/ports/%d/targetPort", i)] = true
		}
	}
	if len(need) == 0 || len(need) != len(mappings) {
		return errors.New("explicit mappings must cover each approved Service routing field exactly once")
	}
	seenField, seenValues := map[string]bool{}, map[string]bool{}
	for _, mapping := range mappings {
		if !need[mapping.FieldPointer] || seenField[mapping.FieldPointer] || seenValues[mapping.ValuesPointer] {
			return errors.New("Service mapping is duplicated or out of scope")
		}
		seenField[mapping.FieldPointer], seenValues[mapping.ValuesPointer] = true, true
	}
	return nil
}
