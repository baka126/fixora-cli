package analyzer

import (
	"fmt"
	"strconv"
	"time"
)

func (a Analyzer) analyzeIngressBackends(ctx *ScanContext) ([]Finding, error) {
	ingresses, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "ingresses")
	if err != nil {
		return nil, err
	}
	services, err := ctx.GetResourceItems(a.opts.Namespace, a.opts.AllNS, "services")
	if err != nil {
		return nil, err
	}
	serviceSet := map[string]bool{}
	serviceByKey := map[string]map[string]any{}
	for _, service := range services {
		serviceSet[objectKey(service)] = true
		serviceByKey[objectKey(service)] = service
	}
	out := []Finding{}
	for _, ingress := range ingresses {
		namespace, name := objectNamespaceName(ingress)
		spec := nestedMap(ingress, "spec")
		if strValue(spec["ingressClassName"]) == "" {
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/MissingClass"),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "MissingIngressClass",
				Severity:     "low",
				Category:     "networking",
				Summary:      "Ingress does not declare spec.ingressClassName.",
				Evidence:     []Evidence{{Label: "IngressClass", Value: "empty"}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Pin the intended ingress controller",
					Description:   "Set spec.ingressClassName in the GitOps source so routing ownership is explicit across clusters.",
					PatchType:     "ingress",
					SafeByDefault: true,
				}},
			})
		}
		for _, backend := range ingressBackendServices(spec) {
			if backend == "" || serviceSet[keyFor(namespace, backend)] {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/MissingService/"+backend),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "MissingBackendService",
				Severity:     "high",
				Category:     "networking",
				Summary:      "Ingress references a backend Service that does not exist in the same namespace.",
				Evidence:     []Evidence{{Label: "Missing service", Value: backend}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Restore or retarget the backend Service",
					Description:   "Create the expected Service or update the Ingress backend reference after confirming the intended workload and release source.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
		for _, ref := range ingressBackendRefs(spec) {
			service, ok := serviceByKey[keyFor(namespace, ref.Service)]
			if !ok {
				continue // a missing Service is already reported by the existence loop
			}
			numbers, names := serviceExposedPorts(service)
			var requested string
			mismatch := false
			if ref.PortName != "" {
				requested = ref.PortName + " (named)"
				mismatch = !names[ref.PortName]
			} else if ref.PortNumber != 0 {
				requested = fmt.Sprint(ref.PortNumber)
				mismatch = !numbers[ref.PortNumber]
			}
			if !mismatch {
				continue
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/IngressPortMismatch/"+ref.Service+"/"+requested),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       "IngressPortMismatch",
				Severity:     "high",
				Category:     "networking",
				Summary:      "Ingress references a Service port the Service does not expose.",
				Evidence: []Evidence{
					{Label: "Backend service", Value: ref.Service},
					{Label: "Requested port", Value: requested},
					{Label: "Service exposed ports", Value: portsLabel(numbers, names)},
				},
				GitOps: gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Retarget the Ingress backend port",
					Description:   "The Ingress backend references a port the Service does not expose. Update the Ingress backend port or the Service port at the source manifest after confirming the intended target.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
		for _, secretName := range ingressTLSSecretNames(spec) {
			if secretName == "" {
				continue
			}
			state := a.objectNameState(ctx, namespace, "secret", secretName)
			if state.Exists {
				if a.opts.CheckCertExpiry {
					out = append(out, a.tlsCertExpiryFindings(ctx, ingress, namespace, name, secretName)...)
				}
				continue
			}
			status := "MissingTLSSecret"
			summary := "Ingress references a TLS Secret that does not exist."
			if state.Forbidden {
				status = "TLSSecretUnreadable"
				summary = "Ingress references a TLS Secret that exists but is not readable with current RBAC."
			}
			out = append(out, Finding{
				ID:           keyFor(namespace, "Ingress/"+name+"/"+status+"/"+secretName),
				Namespace:    namespace,
				ResourceKind: "Ingress",
				ResourceName: name,
				Status:       status,
				Severity:     "high",
				Category:     "networking",
				Summary:      summary,
				Evidence:     []Evidence{{Label: "TLS secret", Value: secretName}, {Label: "State", Value: state.Message}},
				GitOps:       gitOpsForObject(ingress),
				Recommendations: []Recommendation{{
					Title:         "Restore the TLS Secret reference",
					Description:   "If missing, restore the Secret through the certificate workflow. If unreadable, grant read diagnostics RBAC before generating fixes. Fixora does not read Secret values.",
					PatchType:     "ingress",
					SafeByDefault: false,
				}},
			})
		}
	}
	return out, nil
}

type ingressBackendRef struct {
	Service    string
	PortNumber int
	PortName   string
}

// ingressBackendRefs extracts (service, port) references from an Ingress spec,
// covering networking/v1 (backend.service.port.number|name) and the legacy
// extensions/v1beta1 backend.servicePort.
func ingressBackendRefs(spec map[string]any) []ingressBackendRef {
	var out []ingressBackendRef
	add := func(backend map[string]any) {
		name := backendServiceName(backend)
		if name == "" {
			return
		}
		ref := ingressBackendRef{Service: name}
		port := nestedMap(nestedMap(backend, "service"), "port")
		if num := port["number"]; num != nil {
			ref.PortNumber = intValue(num)
		}
		ref.PortName = strValue(port["name"])
		if ref.PortNumber == 0 && ref.PortName == "" {
			if sp := backend["servicePort"]; sp != nil {
				if s, isStr := sp.(string); isStr {
					if n, convErr := strconv.Atoi(s); convErr == nil {
						ref.PortNumber = n
					} else {
						ref.PortName = s
					}
				} else {
					ref.PortNumber = intValue(sp)
				}
			}
		}
		out = append(out, ref)
	}
	add(nestedMap(spec, "defaultBackend"))
	for _, rule := range nestedSlice(spec, "rules") {
		ruleMap, _ := rule.(map[string]any)
		http := nestedMap(ruleMap, "http")
		for _, path := range nestedSlice(http, "paths") {
			pathMap, _ := path.(map[string]any)
			add(nestedMap(pathMap, "backend"))
		}
	}
	return out
}

// serviceExposedPorts returns the numeric and named ports a Service exposes.
func serviceExposedPorts(service map[string]any) (numbers map[int]bool, names map[string]bool) {
	numbers = map[int]bool{}
	names = map[string]bool{}
	for _, p := range nestedSlice(nestedMap(service, "spec"), "ports") {
		pm, _ := p.(map[string]any)
		if n := intValue(pm["port"]); n != 0 {
			numbers[n] = true
		}
		if name := strValue(pm["name"]); name != "" {
			names[name] = true
		}
	}
	return numbers, names
}

const certRecDescription = "The certificate in this Secret is expired or near expiry. Renew it through your certificate workflow (cert-manager, ACME, or manual rotation) and update the Secret. Fixora reads only the public tls.crt for expiry and never the private key, and it cannot mint certificates."

// tlsCertExpiryFindings reads ONLY the public tls.crt from the named TLS Secret
// (never tls.key) and flags expired / soon-to-expire certificates. Returns at
// most one finding. The server certificate is public — sent in cleartext on
// every TLS handshake — which is the carve-out that permits this read.
func (a Analyzer) tlsCertExpiryFindings(ctx *ScanContext, ingress map[string]any, namespace, ingressName, secretName string) []Finding {
	unreadable := func(detail string) []Finding {
		return []Finding{{
			ID:           keyFor(namespace, "Ingress/"+ingressName+"/TLSCertUnreadable/"+secretName),
			Namespace:    namespace,
			ResourceKind: "Ingress",
			ResourceName: ingressName,
			Status:       "TLSCertUnreadable",
			Severity:     "medium",
			Category:     "networking",
			Summary:      "Ingress TLS Secret exists but its certificate could not be read or parsed for expiry.",
			Evidence:     []Evidence{{Label: "TLS secret", Value: secretName}, {Label: "Detail", Value: detail}},
			GitOps:       gitOpsForObject(ingress),
			Recommendations: []Recommendation{{
				Title: "Renew or inspect the TLS certificate", Description: certRecDescription,
				PatchType: "ingress", SafeByDefault: false,
			}},
		}}
	}
	secret, err := ctx.GetResource(namespace, "secret/"+secretName)
	if err != nil {
		return unreadable(err.Error())
	}
	crt, ok := tlsCrtBytes(secret)
	if !ok {
		return unreadable("tls.crt not present or not decodable")
	}
	notAfter, cn, err := leafCertNotAfter(crt)
	if err != nil {
		return unreadable(err.Error())
	}
	status, severity, flag := classifyCertExpiry(notAfter, time.Now())
	if !flag {
		return nil
	}
	summary := "Ingress TLS certificate is expiring soon."
	if status == "TLSCertExpired" {
		summary = "Ingress TLS certificate has expired."
	}
	days := int(time.Until(notAfter).Hours() / 24)
	expiryLabel, expiryValue := "Days remaining", strconv.Itoa(days)
	if days < 0 {
		expiryLabel, expiryValue = "Expired", strconv.Itoa(-days)+" days ago"
	}
	return []Finding{{
		ID:           keyFor(namespace, "Ingress/"+ingressName+"/"+status+"/"+secretName),
		Namespace:    namespace,
		ResourceKind: "Ingress",
		ResourceName: ingressName,
		Status:       status,
		Severity:     severity,
		Category:     "networking",
		Summary:      summary,
		Evidence: []Evidence{
			{Label: "TLS secret", Value: secretName},
			{Label: "Not after", Value: notAfter.UTC().Format(time.RFC3339)},
			{Label: expiryLabel, Value: expiryValue},
			{Label: "Subject", Value: cn},
		},
		GitOps: gitOpsForObject(ingress),
		Recommendations: []Recommendation{{
			Title: "Renew the TLS certificate", Description: certRecDescription,
			PatchType: "ingress", SafeByDefault: false,
		}},
	}}
}
