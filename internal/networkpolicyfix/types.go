// Package networkpolicyfix owns exact, operator-authorized NetworkPolicy
// replacement and its isolated verification lifecycle.
package networkpolicyfix

import (
	"encoding/json"
	"os"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
)

const maxAuthorityBytes = 1 << 20

type Target struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type WorkloadRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type ConnectivityCase struct {
	Name                       string            `json:"name"`
	Direction                  string            `json:"direction"`
	SourcePodLabels            map[string]string `json:"sourcePodLabels"`
	SourceNamespaceLabels      map[string]string `json:"sourceNamespaceLabels"`
	DestinationPodLabels       map[string]string `json:"destinationPodLabels"`
	DestinationNamespaceLabels map[string]string `json:"destinationNamespaceLabels"`
	Port                       int32             `json:"port"`
	Expected                   string            `json:"expected"`
}

type authorityDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Target            Target             `json:"target"`
		Workload          WorkloadRef        `json:"workload"`
		NetworkPolicySpec json.RawMessage    `json:"networkPolicySpec"`
		Verification      verificationIntent `json:"verification"`
	} `json:"spec"`
}

type verificationIntent struct {
	ProbeImage string             `json:"probeImage"`
	Timeout    string             `json:"timeout"`
	Cases      []ConnectivityCase `json:"cases"`
}

// Authority binds one exact policy document and the file identity from which
// it was loaded. All writable intent remains private.
type Authority struct {
	path       string
	digest     [32]byte
	identity   os.FileInfo
	intent     authorityDocument
	policy     networkingv1.NetworkPolicySpec
	policyJSON json.RawMessage
	timeout    time.Duration
}
