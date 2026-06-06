package analyzer

import (
	"reflect"
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
		{name: "ingress backend", resource: "ingress/api", want: []string{"pod", "service", "ingress", "gateway", "networking"}},
		{name: "hpa", resource: "hpa/api", want: []string{"pod", "hpa", "deployment", "statefulset", "daemonset"}},
		{name: "storage", resource: "pvc/data", want: []string{"pod", "pvc", "storage", "storageclass", "node"}},
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
	got := DefaultIncidentFilters(false)
	for _, want := range []string{"pod", "deployment", "service", "hpa", "pvc"} {
		if !containsString(got, want) {
			t.Fatalf("default filters missing %q: %#v", want, got)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
