package checks

import (
	"testing"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
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

func Test__IsJobRunning(t *testing.T) {
	t.Run("1.24+, ready flag unset => false", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		ready := int32(0)
		minorVersionFn := func() int { return 24 }
		j := &batchv1.Job{
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		require.False(t, IsJobRunning(clientset, klog.Background(), j, minorVersionFn))
	})

	t.Run("1.24+, ready flag set => true", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		ready := int32(1)
		minorVersionFn := func() int { return 24 }
		j := &batchv1.Job{
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		require.True(t, IsJobRunning(clientset, klog.Background(), j, minorVersionFn))
	})

	t.Run("<1.24, pod does not exist => false", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		ready := int32(1)
		minorVersionFn := func() int { return 23 }
		j := &batchv1.Job{
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		require.False(t, IsJobRunning(clientset, klog.Background(), j, minorVersionFn))
	})

	t.Run("<1.24, pending pod exists => false", func(t *testing.T) {
		jobName := "job1"
		clientset := fake.NewSimpleClientset([]runtime.Object{
			&corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{"job-name": jobName},
				},
			},
		}...)

		ready := int32(1)
		minorVersionFn := func() int { return 23 }
		j := &batchv1.Job{
			ObjectMeta: v1.ObjectMeta{Name: jobName},
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		require.False(t, IsJobRunning(clientset, klog.Background(), j, minorVersionFn))
	})

	t.Run("<1.24, running pod exists => true", func(t *testing.T) {
		jobName := "job1"
		clientset := fake.NewSimpleClientset([]runtime.Object{
			&corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{"job-name": jobName},
				},
			},
		}...)

		ready := int32(1)
		minorVersionFn := func() int { return 23 }
		j := &batchv1.Job{
			ObjectMeta: v1.ObjectMeta{Name: jobName},
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		require.True(t, IsJobRunning(clientset, klog.Background(), j, minorVersionFn))
	})
}
