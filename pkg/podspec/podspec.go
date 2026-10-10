package podspec

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// Spec holds the fields of the agent pod that decide where it is scheduled.
// Everything else in the agent pod is owned by the controller.
type Spec struct {
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	PriorityClassName         string                            `json:"priorityClassName,omitempty"`
}

// Parse reads a spec from YAML or JSON, and returns nil for an empty document.
// Unknown fields are rejected rather than ignored, so a typo cannot
// silently leave the agent pod free to be scheduled on any node.
func Parse(raw string) (*Spec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var spec Spec
	if err := yaml.UnmarshalStrict([]byte(raw), &spec); err != nil {
		return nil, err
	}

	return &spec, nil
}

// ApplyTo sets the spec's fields on a pod spec. The same spec is
// reused for every job, so the pod gets copies, not shared values.
func (s *Spec) ApplyTo(pod *corev1.PodSpec) {
	if s == nil {
		return
	}

	scheduling := (&corev1.PodSpec{
		NodeSelector:              s.NodeSelector,
		Affinity:                  s.Affinity,
		Tolerations:               s.Tolerations,
		TopologySpreadConstraints: s.TopologySpreadConstraints,
	}).DeepCopy()

	pod.NodeSelector = scheduling.NodeSelector
	pod.Affinity = scheduling.Affinity
	pod.Tolerations = scheduling.Tolerations
	pod.TopologySpreadConstraints = scheduling.TopologySpreadConstraints
	pod.PriorityClassName = s.PriorityClassName
}
