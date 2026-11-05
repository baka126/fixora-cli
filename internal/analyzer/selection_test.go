package analyzer

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestSmartFiltersForSelectsRelatedBuiltIns(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		status   string
		want     []string
	}{
		{name: "service endpoints", resource: "service/api", status: "NoEndpoints", want: []string{"pod", "service", "networking"}},
		{name: "network policy", resource: "networkpolicy/deny-api", status: "EgressBlocked", want: []string{"pod", "networkpolicy", "service", "networking"}},
		{name: "configmap", resource: "configmap/api-config", status: "StaleConfig", want: []string{"pod", "configmap", "deployment", "statefulset", "daemonset"}},
		{name: "ingress backend", resource: "ingress/api", want: []string{"pod", "service", "ingress", "gateway", "networking"}},
		{name: "hpa", resource: "hpa/api", want: []string{"pod", "hpa", "deployment", "statefulset", "daemonset"}},
		{name: "storage", resource: "pvc/data", want: []string{"pod", "pvc", "storage", "storageclass", "node"}},
		{name: "olm", resource: "subscription/operators", status: "UpgradePending", want: []string{"pod", "olm", "operator", "deployment", "service"}},
		{name: "workload", resource: "deployment/api", want: []string{"pod", "deployment", "service", "hpa", "pdb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SmartFiltersFor(tt.resource, tt.status); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("filters=%#v want %#v", got, tt.want)
			}
		})
	}
}

func TestDefaultIncidentFilters(t *testing.T) {
	if got := DefaultIncidentFilters(true); !reflect.DeepEqual(got, []string{"pod"}) {
		t.Fatalf("quick filters=%#v", got)
	}
	if got := DefaultIncidentFilters(false); !reflect.DeepEqual(got, []string{"pod"}) {
		t.Fatalf("default incident filters should be pod-only, got %#v", got)
	}
	got := ComprehensiveDiagnosticFilters()
	for _, want := range []string{"pod", "deployment", "service", "hpa", "pvc", "networkpolicy", "configmap"} {
		if !containsString(got, want) {
			t.Fatalf("comprehensive filters missing %q: %#v", want, got)
		}
	}
	// Comprehensive (used by the health command) must still select pod-security
	// hygiene checks; its aliases are pod-security/podsecurity/security.
	if !matchesAny(filterSet(got), "pod-security", "podsecurity", "security") {
		t.Fatalf("comprehensive filters no longer select pod-security: %#v", got)
	}
}

func TestAllDiagnosticFiltersCoverRegistryAndPrecisionAliases(t *testing.T) {
	filters := filterSet(AllDiagnosticFilters())
	for _, def := range registry {
		for _, value := range []string{def.Name, def.Kind, def.Resource} {
			if value != "" && !filters[valueLower(value)] {
				t.Errorf("all diagnostic filters missing registry alias %q", value)
			}
		}
	}
	for _, alias := range allPrecisionAnalyzerAliases() {
		if !filters[valueLower(alias)] {
			t.Errorf("all diagnostic filters missing precision alias %q", alias)
		}
	}
}

func TestAskFiltersForRequestSelectsByQuestionAndBroadensUnknowns(t *testing.T) {
	tests := []struct {
		name    string
		request string
		quick   bool
		want    []string
		all     bool
	}{
		{name: "networking issue", request: "Why is the service unreachable?", want: []string{"pod", "service", "networking"}},
		{name: "storage issue", request: "Why is this PVC pending?", want: []string{"pod", "pvc", "storage", "storageclass", "node"}},
		{name: "unknown target broadens", request: "Why is api not working?", all: true},
		{name: "broad cluster inventory", request: "What is unhealthy across the cluster?", all: true},
		{name: "quick remains reduced", request: "What is unhealthy across the cluster?", quick: true, want: []string{"pod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AskFiltersForRequest(tt.request, tt.quick)
			if tt.all {
				if !reflect.DeepEqual(got, AllDiagnosticFilters()) {
					t.Fatalf("filters=%#v want full catalog", got)
				}
				return
			}
			for _, want := range tt.want {
				if !containsString(got, want) {
					t.Errorf("filters=%#v missing %q", got, want)
				}
			}
		})
	}
}

func TestAskInventoryDefinitionsFollowSelectedScope(t *testing.T) {
	tests := []struct {
		name    string
		filters []string
		want    []string
	}{
		{name: "pod only", filters: []string{"pod"}, want: []string{"Pod"}},
		{name: "networking group", filters: []string{"pod", "networking"}, want: []string{"Gateway", "GatewayClass", "HTTPRoute", "Ingress", "NetworkPolicy", "Pod", "Service"}},
		{name: "all checks", filters: AllDiagnosticFilters(), want: registryNames()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defs := AskInventoryDefinitions(tt.filters)
			got := make([]string, 0, len(defs))
			for _, def := range defs {
				got = append(got, def.Name)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("inventory definitions=%#v want %#v", got, tt.want)
			}
		})
	}
}

func registryNames() []string {
	names := make([]string, 0, len(registry))
	for _, def := range registry {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names
}

func valueLower(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
