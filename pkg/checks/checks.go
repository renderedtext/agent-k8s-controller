package checks

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func IsJobRunning(clientset kubernetes.Interface, logger logr.Logger, job *batchv1.Job, minorVersionFn func() int) bool {
	//
	// status.ready is behind a feature gate 'JobReadyPods',
	// which is present since 1.23, and enabled by default since Kubernetes 1.24.
	// See: https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/
	//
	if minorVersionFn() >= 24 {
		if job.Status.Ready != nil && *job.Status.Ready > 0 {
			return true
		}

		return false
	}

	//
	// For Kubernetes versions <= 1.23,
	// we need to check the pod directly.
	//
	ok, err := RunningPodExists(clientset, logger, job.Namespace, job.Name)
	if err != nil {
		logger.Error(err, "error checking if pod exists")
		return false
	}

	return ok
}

func RunningPodExists(clientset kubernetes.Interface, logger logr.Logger, namespace, jobName string) (bool, error) {
	//
	// The built-in Kubernetes job controller adds the 'job-name'
	// label to the pods it creates for its jobs, so we use it here,
	// to find the one we are interested in.
	//
	pods, err := clientset.CoreV1().
		Pods(namespace).
		List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("job-name=%s", jobName),
		})

	if err != nil {
		return false, fmt.Errorf("error listing pods: %v", err)
	}

	// pod does not exist
	if len(pods.Items) == 0 {
		logger.Info("Pod does not exist")
		return false, nil
	}

	return IsPodRunning(logger, pods.Items[0]), nil
}

func IsPodRunning(logger logr.Logger, pod corev1.Pod) bool {
	// if one of the pod's containers isn't ready, the pod is not running.
	for _, container := range pod.Status.ContainerStatuses {
		if !container.Ready {
			logger.Info("Container is not ready", "container", container)
			return false
		}
	}

	logger.Info("Pod status", "status", pod.Status.Phase)
	return pod.Status.Phase == corev1.PodRunning
}
