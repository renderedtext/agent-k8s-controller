package podspec

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test__Parse(t *testing.T) {
	t.Run("empty -> no spec", func(t *testing.T) {
		for _, raw := range []string{"", "  ", "\n", "null", "~", "# only a comment\n", "---\n"} {
			spec, err := Parse(raw)
			require.NoError(t, err, raw)
			require.Nil(t, spec, raw)
		}
	})

	t.Run("leading document marker and comments", func(t *testing.T) {
		spec, err := Parse("---\n# agents\nnodeSelector:\n  pool: agents\n")
		require.NoError(t, err)
		require.Equal(t, map[string]string{"pool": "agents"}, spec.NodeSelector)
	})

	t.Run("quoted values are kept as written", func(t *testing.T) {
		spec, err := Parse(`
nodeSelector:
  spot: "yes"
  zone: "012"
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: spot
              operator: NotIn
              values: ["yes", "1.10"]
`)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"spot": "yes", "zone": "012"}, spec.NodeSelector)
		require.Equal(t, []string{"yes", "1.10"},
			spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0].Values)
	})

	t.Run("unquoted value YAML reads as another type -> error", func(t *testing.T) {
		for _, raw := range []string{
			"nodeSelector:\n  spot: yes\n",
			"nodeSelector:\n  spot: true\n",
			"nodeSelector:\n  zone: 012\n",
			"nodeSelector:\n  version: 1.10\n",
			`{"nodeSelector":{"spot":true}}`,
			"affinity:\n  nodeAffinity:\n    requiredDuringSchedulingIgnoredDuringExecution:\n      nodeSelectorTerms:\n" +
				"        - matchExpressions:\n            - key: spot\n              operator: NotIn\n              values: [yes]\n",
		} {
			_, err := Parse(raw)
			require.ErrorContains(t, err, "cannot unmarshal", raw)
		}
	})

	t.Run("field name in another case -> error", func(t *testing.T) {
		_, err := Parse("NodeSelector:\n  pool: agents\n")
		require.ErrorContains(t, err, `unknown field "NodeSelector"`)
	})

	t.Run("YAML with every supported field", func(t *testing.T) {
		spec, err := Parse(`
nodeSelector:
  pool: agents
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              semaphoreci.com/resource-type: semaphore-job
tolerations:
  - key: dedicated
    operator: Equal
    value: agents
    effect: NoSchedule
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        semaphoreci.com/resource-type: semaphore-job
priorityClassName: semaphore-agents
`)

		require.NoError(t, err)
		require.Equal(t, map[string]string{"pool": "agents"}, spec.NodeSelector)
		require.Equal(t, "kubernetes.io/hostname",
			spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm.TopologyKey)
		require.Equal(t, []corev1.Toleration{{
			Key:      "dedicated",
			Operator: corev1.TolerationOpEqual,
			Value:    "agents",
			Effect:   corev1.TaintEffectNoSchedule,
		}}, spec.Tolerations)
		require.Len(t, spec.TopologySpreadConstraints, 1)
		require.Equal(t, corev1.ScheduleAnyway, spec.TopologySpreadConstraints[0].WhenUnsatisfiable)
		require.Equal(t, "semaphore-agents", spec.PriorityClassName)
	})

	t.Run("JSON", func(t *testing.T) {
		spec, err := Parse(`{"nodeSelector":{"pool":"agents"},"tolerations":[{"key":"dedicated","operator":"Exists"}]}`)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"pool": "agents"}, spec.NodeSelector)
		require.Equal(t, corev1.TolerationOpExists, spec.Tolerations[0].Operator)
	})

	t.Run("empty object -> empty spec", func(t *testing.T) {
		spec, err := Parse("{}")
		require.NoError(t, err)
		require.NotNil(t, spec)
		require.Equal(t, Spec{}, *spec)
	})

	t.Run("pod field that is not supported -> error", func(t *testing.T) {
		_, err := Parse("serviceAccountName: other\n")
		require.ErrorContains(t, err, `unknown field "serviceAccountName"`)
	})

	t.Run("misspelled field -> error", func(t *testing.T) {
		_, err := Parse("nodeSelectors:\n  pool: agents\n")
		require.ErrorContains(t, err, `unknown field "nodeSelectors"`)
	})

	t.Run("misspelled nested field -> error", func(t *testing.T) {
		_, err := Parse(`
affinity:
  nodeAffinity:
    requiredDuringScheduling:
      nodeSelectorTerms: []
`)
		require.ErrorContains(t, err, `unknown field "affinity.nodeAffinity.requiredDuringScheduling"`)
	})

	t.Run("duplicate field -> error", func(t *testing.T) {
		_, err := Parse("priorityClassName: a\npriorityClassName: b\n")
		require.Error(t, err)
	})

	t.Run("wrong type -> error", func(t *testing.T) {
		_, err := Parse("tolerations:\n  key: dedicated\n")
		require.Error(t, err)
	})

	t.Run("not an object -> error", func(t *testing.T) {
		_, err := Parse("- nodeSelector: {}\n")
		require.Error(t, err)
	})
}

func Test__ApplyTo(t *testing.T) {
	t.Run("no spec -> pod is not changed", func(t *testing.T) {
		pod := corev1.PodSpec{ServiceAccountName: "agent", RestartPolicy: corev1.RestartPolicyNever}
		var spec *Spec
		spec.ApplyTo(&pod)
		require.Equal(t, corev1.PodSpec{ServiceAccountName: "agent", RestartPolicy: corev1.RestartPolicyNever}, pod)
	})

	t.Run("sets the scheduling fields only", func(t *testing.T) {
		spec := &Spec{
			NodeSelector: map[string]string{"pool": "agents"},
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "pool",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"agents"},
							}},
						}},
					},
				},
			},
			Tolerations: []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       "kubernetes.io/hostname",
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}},
			}},
			PriorityClassName: "semaphore-agents",
		}

		pod := corev1.PodSpec{
			ServiceAccountName: "agent",
			RestartPolicy:      corev1.RestartPolicyNever,
			Containers:         []corev1.Container{{Name: "semaphore-agent"}},
		}

		spec.ApplyTo(&pod)

		require.Equal(t, corev1.PodSpec{
			ServiceAccountName:        "agent",
			RestartPolicy:             corev1.RestartPolicyNever,
			Containers:                []corev1.Container{{Name: "semaphore-agent"}},
			NodeSelector:              spec.NodeSelector,
			Affinity:                  spec.Affinity,
			Tolerations:               spec.Tolerations,
			TopologySpreadConstraints: spec.TopologySpreadConstraints,
			PriorityClassName:         "semaphore-agents",
		}, pod)
	})

	t.Run("pod does not share values with the spec", func(t *testing.T) {
		spec := &Spec{
			NodeSelector: map[string]string{"pool": "agents"},
			Affinity:     &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}},
			Tolerations:  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}},
			}},
		}

		pod := corev1.PodSpec{}
		spec.ApplyTo(&pod)

		pod.NodeSelector["pool"] = "changed"
		pod.Affinity.PodAffinity = &corev1.PodAffinity{}
		pod.Tolerations[0].Key = "changed"
		pod.TopologySpreadConstraints[0].LabelSelector.MatchLabels["a"] = "changed"

		require.Equal(t, "agents", spec.NodeSelector["pool"])
		require.Nil(t, spec.Affinity.PodAffinity)
		require.Equal(t, "dedicated", spec.Tolerations[0].Key)
		require.Equal(t, "b", spec.TopologySpreadConstraints[0].LabelSelector.MatchLabels["a"])
	})
}
