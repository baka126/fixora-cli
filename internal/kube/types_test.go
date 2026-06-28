package kube

import (
	"encoding/json"
	"testing"
)

func TestPodUnmarshalsPortsAndReadinessGates(t *testing.T) {
	const data = `{
	  "spec": {
	    "readinessGates": [{"conditionType": "example.com/feature-1"}],
	    "containers": [{
	      "name": "app",
	      "ports": [
	        {"name": "http", "containerPort": 8080, "protocol": "TCP"},
	        {"containerPort": 9090}
	      ]
	    }]
	  }
	}`
	var pod Pod
	if err := json.Unmarshal([]byte(data), &pod); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pod.Spec.ReadinessGates) != 1 || pod.Spec.ReadinessGates[0].ConditionType != "example.com/feature-1" {
		t.Fatalf("readiness gates not parsed: %+v", pod.Spec.ReadinessGates)
	}
	ports := pod.Spec.Containers[0].Ports
	if len(ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(ports))
	}
	if ports[0].Name != "http" || ports[0].ContainerPort != 8080 || ports[0].Protocol != "TCP" {
		t.Fatalf("port 0 wrong: %+v", ports[0])
	}
	if ports[1].ContainerPort != 9090 {
		t.Fatalf("port 1 wrong: %+v", ports[1])
	}
}
