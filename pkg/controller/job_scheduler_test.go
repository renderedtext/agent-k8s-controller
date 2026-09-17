package controller

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
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

	t.Run("non-running job is loaded on startup", func(t *testing.T) {
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

	t.Run("running job is loaded on startup", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		jobID := randJobID()
		require.False(t, scheduler.IsCurrentJob(jobID))

		ready := int32(1)
		j := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					config.JobIDLabel:     jobID,
					config.AgentTypeLabel: agentType.AgentTypeName,
				},
			},
			Status: batchv1.JobStatus{
				Ready: &ready,
			},
		}

		scheduler.OnAdd(j, false)
		require.True(t, scheduler.IsCurrentJob(jobID))
		require.True(t, scheduler.current[jobID].Running)
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

	t.Run("job that starts after not starting in time is ignored", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// job is created
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// job does not start in time, so we stop tracking it and delete it
		j2 := j.DeepCopy()
		j2.CreationTimestamp = metav1.Time{Time: time.Now().Add(-2 * time.Minute)}
		scheduler.OnUpdate(j, j2)
		jobDoesNotExist(t, scheduler, clientset, jobID)

		// an update saying the job started arrives after that,
		// because the deletion wasn't observed by the informer yet
		ready := int32(1)
		j3 := j2.DeepCopy()
		j3.Status.Ready = &ready
		j3.Status.StartTime = &metav1.Time{Time: time.Now()}
		require.NotPanics(t, func() { scheduler.OnUpdate(j2, j3) })
		require.False(t, scheduler.IsCurrentJob(jobID))
	})

	t.Run("running job without a start time is marked as started", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// job is created and is still tracked
		jobID := randJobID()
		req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
		require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
		j := jobExists(t, scheduler, clientset, jobID)
		require.True(t, scheduler.IsCurrentJob(jobID))

		// the job reports ready pods, but no start time yet
		ready := int32(1)
		j2 := j.DeepCopy()
		j2.Status.Ready = &ready
		require.Nil(t, j2.Status.StartTime)

		require.NotPanics(t, func() { scheduler.OnUpdate(j, j2) })
		require.True(t, scheduler.current[jobID].Running)
	})

	t.Run("job is not created if limit was reached", func(t *testing.T) {
		clear(scheduler.current)
		defer clear(scheduler.current)

		// create jobs up to max
		require.True(t, scheduler.HasSpace())
		for i := 0; i < maxParallelJobs; i++ {
			jobID := randJobID()
			req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
			require.NoError(t, scheduler.Create(context.Background(), req, &agentType))
			_ = jobExists(t, scheduler, clientset, jobID)
			require.True(t, scheduler.IsCurrentJob(jobID))
		}

		// no more space available
		require.False(t, scheduler.HasSpace())

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

func Test__JobSchedulerIsSafeForConcurrentUse(t *testing.T) {
	agentType := agenttypes.AgentType{
		AgentTypeName:          "s1-test",
		RegistrationToken:      "very-sensitive-token",
		AgentStartupParameters: []string{},
	}

	clientset := newFakeClientset(t, []runtime.Object{})
	scheduler, err := NewJobScheduler(clientset, &config.Config{
		Namespace:              "default",
		AgentImage:             "semaphoreci/agent:latest",
		AgentStartupParameters: []string{},
		Labels:                 []string{},
		MaxParallelJobs:        200,
		JobStartTimeout:        time.Minute,
	})

	require.NoError(t, err)

	//
	// The informer handlers and the controller's tick run on different
	// goroutines and both touch the scheduler's job map. Run them against
	// each other so that '-race' can catch an unguarded access.
	//
	jobIDs := make([]string, 50)
	for i := range jobIDs {
		jobIDs[i] = randJobID()
	}

	ready := int32(1)
	var wg sync.WaitGroup

	for _, jobID := range jobIDs {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: scheduler.jobName(jobID),
				Labels: map[string]string{
					config.JobIDLabel:     jobID,
					config.AgentTypeLabel: agentType.AgentTypeName,
				},
				CreationTimestamp: metav1.Time{Time: time.Now()},
			},
			Status: batchv1.JobStatus{
				Ready:     &ready,
				StartTime: &metav1.Time{Time: time.Now()},
			},
		}

		wg.Add(4)

		// the controller's tick
		go func(jobID string) {
			defer wg.Done()
			req := semaphore.JobRequest{JobID: jobID, MachineType: agentType.AgentTypeName}
			_ = scheduler.Create(context.Background(), req, &agentType)
			scheduler.HasSpace()
			scheduler.IsCurrentJob(jobID)
		}(jobID)

		// the informer handlers
		go func(job *batchv1.Job) { defer wg.Done(); scheduler.OnAdd(job, false) }(job)
		go func(job *batchv1.Job) { defer wg.Done(); scheduler.OnUpdate(job, job) }(job)
		go func(job *batchv1.Job) { defer wg.Done(); scheduler.OnDelete(job) }(job)
	}

	wg.Wait()
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
	fakeDiscovery.FakedServerVersion = &version.Info{GitVersion: "v1.27.1"}
	return fakeClientset
}

func randJobID() string {
	return fmt.Sprintf("job-%d", rand.Int())
}
