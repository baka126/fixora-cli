package kube

type ObjectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	OwnerRefs   []OwnerReference  `json:"ownerReferences"`
}

type OwnerReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type PodList struct {
	Items []Pod `json:"items"`
}

type Pod struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       PodSpec    `json:"spec"`
	Status     PodStatus  `json:"status"`
}

type PodSpec struct {
	NodeName       string            `json:"nodeName"`
	NodeSelector   map[string]string `json:"nodeSelector"`
	Containers     []Container       `json:"containers"`
	InitContainers []Container       `json:"initContainers"`
	Tolerations    []map[string]any  `json:"tolerations"`
}

type Container struct {
	Name            string               `json:"name"`
	Image           string               `json:"image"`
	ImagePullPolicy string               `json:"imagePullPolicy"`
	Resources       ResourceRequirements `json:"resources"`
	Env             []EnvVar             `json:"env"`
	EnvFrom         []map[string]any     `json:"envFrom"`
	SecurityContext map[string]any       `json:"securityContext"`
	VolumeMounts    []map[string]any     `json:"volumeMounts"`
	LivenessProbe   map[string]any       `json:"livenessProbe"`
	ReadinessProbe  map[string]any       `json:"readinessProbe"`
	StartupProbe    map[string]any       `json:"startupProbe"`
}

type EnvVar struct {
	Name      string         `json:"name"`
	Value     string         `json:"value"`
	ValueFrom map[string]any `json:"valueFrom"`
}

type ResourceRequirements struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

type PodStatus struct {
	Phase             string            `json:"phase"`
	Reason            string            `json:"reason"`
	ContainerStatuses []ContainerStatus `json:"containerStatuses"`
	InitStatuses      []ContainerStatus `json:"initContainerStatuses"`
	Conditions        []Condition       `json:"conditions"`
}

type ContainerStatus struct {
	Name         string                 `json:"name"`
	Ready        bool                   `json:"ready"`
	RestartCount int                    `json:"restartCount"`
	State        map[string]StatusState `json:"state"`
	LastState    map[string]StatusState `json:"lastState"`
}

type StatusState struct {
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	ExitCode   int    `json:"exitCode"`
	FinishedAt string `json:"finishedAt"`
}

type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type EventList struct {
	Items []Event `json:"items"`
}

type Event struct {
	Metadata ObjectMeta `json:"metadata"`
	Type     string     `json:"type"`
	Reason   string     `json:"reason"`
	Message  string     `json:"message"`
	LastTime string     `json:"lastTimestamp"`
}

type NodeList struct {
	Items []Node `json:"items"`
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		ProviderID string           `json:"providerID"`
		Taints     []map[string]any `json:"taints"`
	} `json:"spec"`
	Status struct {
		Allocatable map[string]string `json:"allocatable"`
		Capacity    map[string]string `json:"capacity"`
		Conditions  []Condition       `json:"conditions"`
	} `json:"status"`
}
