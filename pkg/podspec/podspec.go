package podspec

import (
	"bytes"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/json"
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

// Parse reads a spec from YAML or JSON, and returns nil when there is none.
// Unknown fields and values of the wrong type are rejected, not converted,
// so an unquoted `yes` cannot silently become "true" in a label value.
func Parse(raw string) (*Spec, error) {
	// Converting without a target type keeps YAML from coercing values to fit it.
	data, err := yaml.YAMLToJSONStrict([]byte(raw))
	if err != nil {
		return nil, err
	}

	// Empty, null or only comments.
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, nil
	}

	var spec Spec
	strictErrs, err := json.UnmarshalStrict(data, &spec)
	if err != nil {
		return nil, err
	}

	if len(strictErrs) > 0 {
		return nil, errors.Join(strictErrs...)
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
