package controller

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
	"github.com/renderedtext/agent-k8s-stack/pkg/config"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func Test__JobScheduler(t *testing.T) {
	agentType := agenttypes.AgentType{
		AgentTypeName:          "s1-test",
		RegistrationToken:      "very-sensitive-token",
		AgentStartupParameters: []string{},
	}

	maxParallelJobs := 5
	clientset := newFakeClientset([]runtime.Object{})
	scheduler := NewJobScheduler(clientset, &config.Config{
		Namespace:              "default",
		AgentImage:             "semaphoreci/agent:latest",
		AgentStartupParameters: []string{},
		Labels:                 []string{},
		MaxParallelJobs:        maxParallelJobs,
	})

	t.Run("job is loaded on startup", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		jobID := randJobID()
		require.False(t, scheduler.JobExists(jobID))

		j := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					config.JobIDLabel:     jobID,
					config.AgentTypeLabel: agentType.AgentTypeName,
				},
			},
		}

		scheduler.OnAdd(j, false)
		require.True(t, scheduler.JobExists(jobID))
	})

	t.Run("job is created", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		jobID := randJobID()
		jobDoesNotExist(t, scheduler, clientset, jobID)
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		err := scheduler.Create(context.Background(), req, &agentType)
		require.NoError(t, err)
		jobExists(t, scheduler, clientset, jobID)

		// Job creation is idempotent
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
	})

	t.Run("job is not created if limit was reached", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// create jobs up to max
		for i := 0; i < maxParallelJobs; i++ {
			jobID := randJobID()
			req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
			require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
			_ = jobExists(t, scheduler, clientset, jobID)
		}

		// creating a job returns an error now
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		err := scheduler.Create(context.Background(), req, &agentType)
		require.ErrorIs(t, err, ErrParallelJobsLimitReached)
		jobDoesNotExist(t, scheduler, clientset, jobID)
	})

	t.Run("job is deleted", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)

		scheduler.OnDelete(j)
		require.False(t, scheduler.JobExists(jobID))
	})
}

func jobExists(t *testing.T, scheduler *JobScheduler, clientset kubernetes.Interface, jobID string) *batchv1.Job {
	j, err := clientset.BatchV1().Jobs("default").Get(context.Background(), scheduler.jobName(jobID), metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, j)
	require.True(t, scheduler.JobExists(jobID))
	return j
}

func jobDoesNotExist(t *testing.T, scheduler *JobScheduler, clientset kubernetes.Interface, jobID string) {
	_, err := clientset.BatchV1().Jobs("default").Get(context.Background(), scheduler.jobName(jobID), metav1.GetOptions{})
	require.Error(t, err)
	require.True(t, errors.IsNotFound(err))
	require.False(t, scheduler.JobExists(jobID))
}

func newFakeClientset(objects []runtime.Object) kubernetes.Interface {
	return fake.NewSimpleClientset(objects...)
}

func randJobID() string {
	return fmt.Sprintf("job-%d", rand.Int())
}
