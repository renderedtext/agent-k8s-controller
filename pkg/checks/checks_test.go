package checks

import (
	"fmt"
	"testing"

	versions "github.com/hashicorp/go-version"
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
	vs := []string{"v1.24", "v1.24.17", "v1.24.17-eks-c12679a", "v1.25", "v1.26", "v1.27", "v1.28", "v1.29", "v1.30", "v2.0"}

	for _, version := range vs {
		versionFn := func() *versions.Version {
			v, _ := versions.NewVersion(version)
			return v
		}

		t.Run(fmt.Sprintf("%s, ready flag unset => false", version), func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			ready := int32(0)
			j := &batchv1.Job{
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.False(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})

		t.Run(fmt.Sprintf("%s, ready flag set => true", version), func(t *testing.T) {
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
	vs = []string{"v1.18", "v1.19", "v1.20", "v1.21", "v1.22", "v1.23", "v1.23.17", "v1.23.17-eks-c12679a"}

	for _, version := range vs {
		versionFn := func() *versions.Version {
			v, _ := versions.NewVersion(version)
			return v
		}

		t.Run(fmt.Sprintf("%s, pod does not exist => false", version), func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			ready := int32(1)
			j := &batchv1.Job{
				Status: batchv1.JobStatus{
					Ready: &ready,
				},
			}

			require.False(t, IsJobRunning(clientset, klog.Background(), j, versionFn))
		})

		t.Run(fmt.Sprintf("%s, pending pod exists => false", version), func(t *testing.T) {
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

		t.Run(fmt.Sprintf("%s, running pod exists => true", version), func(t *testing.T) {
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
