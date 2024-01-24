package checks

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

func Test__IsPodRunning(t *testing.T) {
	t.Run("container not ready => false", func(t *testing.T) {
		require.False(t, IsPodRunning(klog.Background(), corev1.Pod{
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}},
				},
			},
		}))
	})

	t.Run("pod pending => false", func(t *testing.T) {
		require.False(t, IsPodRunning(klog.Background(), corev1.Pod{
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		}))
	})

	t.Run("containers ready and pod running => false", func(t *testing.T) {
		require.False(t, IsPodRunning(klog.Background(), corev1.Pod{
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
				},
			},
		}))
	})
}
