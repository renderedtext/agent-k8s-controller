package controller

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
	"github.com/renderedtext/agent-k8s-stack/pkg/config"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
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
	clientset := newFakeClientset(t, []runtime.Object{})
	scheduler, err := NewJobScheduler(clientset, &config.Config{
		Namespace:              "default",
		AgentImage:             "semaphoreci/agent:latest",
		AgentStartupParameters: []string{},
		Labels:                 []string{},
		MaxParallelJobs:        maxParallelJobs,
		JobStartTimeout:        time.Minute,
	})

	require.NoError(t, err)

	t.Run("job is loaded on startup", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		jobID := randJobID()
		require.False(t, scheduler.IsCurrentJob(jobID))

		j := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					config.JobIDLabel:     jobID,
					config.AgentTypeLabel: agentType.AgentTypeName,
				},
			},
		}

		scheduler.OnAdd(j, false)
		require.True(t, scheduler.IsCurrentJob(jobID))
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
		require.True(t, scheduler.IsCurrentJob(jobID))

		// Job creation is idempotent
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
	})

	t.Run("job is marked as started", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// job is created
		jobID := randJobID()
		jobDoesNotExist(t, scheduler, clientset, jobID)
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		err := scheduler.Create(context.Background(), req, &agentType)
		require.NoError(t, err)
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// job starts
		ready := int32(1)
		j2 := j.DeepCopy()
		j2.Status.Ready = &ready
		j2.Status.StartTime = &metav1.Time{Time: time.Now()}
		scheduler.OnUpdate(j, j2)
		require.True(t, scheduler.current[jobID].Running)
	})

	t.Run("job is deleted if it doesn't start in time", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		scheduler.config.KeepFailedJobsFor = time.Minute
		defer func() {
			scheduler.config.KeepFailedJobsFor = 0
		}()

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// job does not start in time and is deleted
		j2 := j.DeepCopy()
		twoMinutesAgo := time.Now().Add(-2 * time.Minute)
		j2.CreationTimestamp = metav1.Time{Time: twoMinutesAgo}
		scheduler.OnUpdate(j, j2)
		jobDoesNotExist(t, scheduler, clientset, jobID)
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
			require.True(t, scheduler.IsCurrentJob(jobID))
		}

		// creating a job returns an error now
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		err := scheduler.Create(context.Background(), req, &agentType)
		require.ErrorIs(t, err, ErrParallelJobsLimitReached)
		jobDoesNotExist(t, scheduler, clientset, jobID)
	})

	t.Run("no retention used -> job is deleted", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		scheduler.OnDelete(j)
		require.False(t, scheduler.IsCurrentJob(jobID))
	})

	t.Run("retention for successful job is used", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		scheduler.config.KeepSuccessfulJobsFor = time.Minute
		defer func() {
			scheduler.config.KeepSuccessfulJobsFor = 0
		}()

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// job finishes successfully, but is not deleted
		j2 := j.DeepCopy()
		thirtySecondsAgo := time.Now().Add(-30 * time.Second)
		j2.Status.Conditions = append(j2.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobComplete, Status: v1.ConditionTrue})
		j2.Status.CompletionTime = &metav1.Time{Time: thirtySecondsAgo}
		scheduler.OnUpdate(j, j2)
		_ = jobExists(t, scheduler, clientset, jobID)

		// after some time, it is deleted
		j3 := j2.DeepCopy()
		twoMinutesAgo := time.Now().Add(-2 * time.Minute)
		j3.Status.CompletionTime = &metav1.Time{Time: twoMinutesAgo}
		scheduler.OnUpdate(j2, j3)
		jobDoesNotExist(t, scheduler, clientset, jobID)
	})

	t.Run("retention for failed job is used", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		scheduler.config.KeepFailedJobsFor = time.Minute
		defer func() {
			scheduler.config.KeepFailedJobsFor = 0
		}()

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// job finishes successfully, but is not deleted
		j2 := j.DeepCopy()
		thirtySecondsAgo := time.Now().Add(-30 * time.Second)
		j2.Status.Conditions = append(j2.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobFailed, Status: v1.ConditionTrue})
		j2.CreationTimestamp = metav1.Time{Time: thirtySecondsAgo}
		scheduler.OnUpdate(j, j2)
		_ = jobExists(t, scheduler, clientset, jobID)

		// after some time, it is deleted
		j3 := j2.DeepCopy()
		twoMinutesAgo := time.Now().Add(-2 * time.Minute)
		j3.CreationTimestamp = metav1.Time{Time: twoMinutesAgo}
		scheduler.OnUpdate(j2, j3)
		jobDoesNotExist(t, scheduler, clientset, jobID)
	})
}

func jobExists(t *testing.T, scheduler *JobScheduler, clientset kubernetes.Interface, jobID string) *batchv1.Job {
	j, err := clientset.BatchV1().Jobs("default").Get(context.Background(), scheduler.jobName(jobID), metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, j)
	return j
}

func jobDoesNotExist(t *testing.T, scheduler *JobScheduler, clientset kubernetes.Interface, jobID string) {
	_, err := clientset.BatchV1().Jobs("default").Get(context.Background(), scheduler.jobName(jobID), metav1.GetOptions{})
	require.Error(t, err)
	require.True(t, errors.IsNotFound(err))
	require.False(t, scheduler.IsCurrentJob(jobID))
}

func newFakeClientset(t *testing.T, objects []runtime.Object) kubernetes.Interface {
	fakeClientset := fake.NewSimpleClientset(objects...)
	fakeDiscovery, ok := fakeClientset.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	fakeDiscovery.FakedServerVersion = &version.Info{Minor: "27"}
	return fakeClientset
}

func randJobID() string {
	return fmt.Sprintf("job-%d", rand.Int())
}
