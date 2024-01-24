package checks

import (
	"fmt"
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
	// test for versions with JobReadyPods feature gate
	versions := [][2]int{{1, 24}, {1, 25}, {1, 26}, {1, 27}, {1, 28}, {1, 29}, {1, 30}, {2, 0}}

	for _, v := range versions {
		versionFn := func() (int, int) { return v[0], v[1] }
		t.Run(fmt.Sprintf("v%d.%d, ready flag unset => false", v[0], v[1]), func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			ready := int32(0)
			j := &batchv1.Job{
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.False(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})

		t.Run(fmt.Sprintf("v%d.%d, ready flag set => true", v[0], v[1]), func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			ready := int32(1)
			j := &batchv1.Job{
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.True(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})
	}

	// test for versions without JobReadyPods feature gate
	versions = [][2]int{{1, 18}, {1, 19}, {1, 20}, {1, 21}, {1, 22}, {1, 23}}

	for _, v := range versions {
		versionFn := func() (int, int) { return v[0], v[1] }

		t.Run(fmt.Sprintf("v%d.%d, pod does not exist => false", v[0], v[1]), func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			ready := int32(1)
			j := &batchv1.Job{
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.False(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})

		t.Run(fmt.Sprintf("v%d.%d, pending pod exists => false", v[0], v[1]), func(t *testing.T) {
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
			j := &batchv1.Job{
				ObjectMeta: v1.ObjectMeta{Name: jobName},
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.False(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})

		t.Run(fmt.Sprintf("v%d.%d, running pod exists => true", v[0], v[1]), func(t *testing.T) {
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
			j := &batchv1.Job{
				ObjectMeta: v1.ObjectMeta{Name: jobName},
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.True(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})
	}
}
