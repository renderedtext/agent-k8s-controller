package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	versions "github.com/hashicorp/go-version"
	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
	checks "github.com/renderedtext/agent-k8s-stack/pkg/checks"
	"github.com/renderedtext/agent-k8s-stack/pkg/config"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

var ErrParallelJobsLimitReached = errors.New("number of parallel jobs reached")

type JobState struct {
	ID        string
	AgentType string
	Running   bool
}

type JobScheduler struct {
	clientset         kubernetes.Interface
	config            *config.Config
	current           map[string]*JobState
	mu                sync.Mutex
	kubernetesVersion *versions.Version
}

func NewJobScheduler(clientset kubernetes.Interface, config *config.Config) (*JobScheduler, error) {
	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return nil, err
	}

	klog.InfoS("Kubernetes version", "version", version)

	v, err := versions.NewVersion(version.String())
	if err != nil {
		return nil, err
	}

	return &JobScheduler{
		current:           map[string]*JobState{},
		clientset:         clientset,
		config:            config,
		kubernetesVersion: v,
	}, nil
}

func (s *JobScheduler) RegisterInformer(informerFactory informers.SharedInformerFactory) error {
	informer := informerFactory.Batch().V1().Jobs()
	_, err := informer.Informer().AddEventHandler(s)
	return err
}

func (s *JobScheduler) HasSpace() bool {
	return len(s.current) < s.config.MaxParallelJobs
}

func (s *JobScheduler) Create(ctx context.Context, req semaphore.JobRequest, agentType *agenttypes.AgentType) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If the job was already created, we don't create it again.
	// This can happen if the time it takes for the agent to start
	// is bigger than the time it takes for the next controller tick to run.
	if _, ok := s.current[req.JobID]; ok {
		return nil
	}

	if len(s.current) == s.config.MaxParallelJobs {
		return ErrParallelJobsLimitReached
	}

	_, err := s.clientset.BatchV1().
		Jobs(s.config.Namespace).
		Create(
			ctx,
			s.buildJob(req, agentType),
			metav1.CreateOptions{},
		)

	if err == nil {
		s.current[req.JobID] = &JobState{
			ID:        req.JobID,
			AgentType: req.MachineType,
			Running:   false,
		}

		klog.InfoS("Job created", "job", req.JobID, "type", req.MachineType)
		return nil
	}

	return err
}

func (s *JobScheduler) jobName(jobID string) string {
	return fmt.Sprintf("semaphore-agent-%s", jobID)
}

func (s *JobScheduler) buildJob(job semaphore.JobRequest, agentType *agenttypes.AgentType) *batchv1.Job {
	parallelism := int32(1)
	retries := int32(0)
	activeDeadlineSeconds := int64(60 * 60 * 24) // 1 day
	terminationGracePeriod := int64(300)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.jobName(job.JobID),
			Namespace: s.config.Namespace,
			Labels:    s.buildLabels(job),
		},
		Spec: batchv1.JobSpec{
			Parallelism:           &parallelism,
			Completions:           &parallelism,
			BackoffLimit:          &retries,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: s.buildLabels(job)},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					ServiceAccountName:            s.config.ServiceAccountName,
					TerminationGracePeriodSeconds: &terminationGracePeriod,
					Containers: []corev1.Container{
						{
							Name:  "semaphore-agent",
							Image: s.config.AgentImage,
							Command: []string{
								"/opt/semaphore/agent",
								"start",
							},
							Args: s.buildAgentStartupParameters(agentType, job.JobID),
							Env: []corev1.EnvVar{
								{
									Name: "KUBERNETES_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
									},
								},
								{
									Name: "SEMAPHORE_AGENT_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
									},
								},
								{
									Name:  "SEMAPHORE_AGENT_LOG_LEVEL",
									Value: s.config.AgentLogLevel,
								},
								{
									Name: "SEMAPHORE_AGENT_TOKEN",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											Key: "registrationToken",
											LocalObjectReference: corev1.LocalObjectReference{
												Name: agentType.SecretName,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (s *JobScheduler) buildLabels(job semaphore.JobRequest) map[string]string {
	labels := map[string]string{
		config.ResourceTypeLabel: config.SemaphoreJobResourceType,
		config.AgentTypeLabel:    job.MachineType,
		config.JobIDLabel:        job.JobID,
	}

	for _, label := range s.config.Labels {
		parts := strings.Split(label, "=")
		labels[parts[0]] = parts[1]
	}

	return labels
}

func (s *JobScheduler) buildAgentStartupParameters(agentType *agenttypes.AgentType, jobID string) []string {
	labels := []string{
		fmt.Sprintf("%s=%s", config.AgentTypeLabel, agentType.AgentTypeName),
	}

	if len(s.config.Labels) > 0 {
		labels = append(labels, s.config.Labels...)
	}

	parameters := []string{
		"--endpoint",
		s.config.SemaphoreEndpoint,
		"--job-id",
		jobID,
		"--kubernetes-labels",
		strings.Join(labels, ","),
		"--kubernetes-executor",
		"--disconnect-after-job",
	}

	// If agent type does not specify startup parameters, use the controller's defaults.
	if len(agentType.AgentStartupParameters) == 0 {
		return append(parameters, s.config.AgentStartupParameters...)
	}

	// Otherwise, use the agent type's startup parameters.
	return append(parameters, agentType.AgentStartupParameters...)
}

func (s *JobScheduler) delete(jobID string) error {
	propagationPolicy := metav1.DeletePropagationBackground
	return s.clientset.BatchV1().
		Jobs(s.config.Namespace).
		Delete(context.Background(), s.jobName(jobID), metav1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
}

// This method executes when a new job is added,
// but also executes for all jobs when the controller starts up.
// If the controller crashed for whatever reason, we reload the jobs.
func (s *JobScheduler) OnAdd(obj interface{}, _ bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := obj.(*batchv1.Job)
	jobID, ok := job.Labels[config.JobIDLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.JobIDLabel)
		return
	}

	agentType, ok := job.Labels[config.AgentTypeLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.AgentTypeLabel)
		return
	}

	if _, ok := s.current[jobID]; !ok {
		logger := klog.LoggerWithValues(klog.Background(), "job", jobID, "type", agentType)
		s.current[jobID] = &JobState{
			ID:        jobID,
			AgentType: agentType,
			Running:   s.isJobRunning(logger, jobID, job),
		}

		logger.Info("Job loaded")
	}
}

// Handles job state transitions
func (s *JobScheduler) OnUpdate(_, obj interface{}) {
	job := obj.(*batchv1.Job)
	jobID, ok := job.Labels[config.JobIDLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.JobIDLabel)
		return
	}

	agentType, ok := job.Labels[config.AgentTypeLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.AgentTypeLabel)
		return
	}

	logger := klog.LoggerWithValues(klog.Background(), "job", jobID, "type", agentType)
	conditions := jobConditions(job)

	//
	// If the "Complete" condition is set for the job,
	// we know it finished successfully.
	//
	if slices.Contains(conditions, batchv1.JobComplete) {
		s.handleSuccessfulJob(logger, jobID, job)
		return
	}

	//
	// If the "Failed" condition is set for the job,
	// we know it failed to complete successfully.
	//
	if slices.Contains(conditions, batchv1.JobFailed) {
		s.handleFailedJob(logger, jobID, job)
		return
	}

	//
	// If the job doesn't have any terminal condition set
	// it is still running.
	//
	s.handleInProgress(logger, jobID, job)
}

func (s *JobScheduler) isJobRunning(logger logr.Logger, jobID string, job *batchv1.Job) bool {
	//
	// Check if we have already marked this job as started.
	// The reason for this check is that there is a small period of time
	// between the pod finishing and the job being marked as complete,
	// where the status.ready counter goes back to 0.
	//
	if s.IsCurrentJob(jobID) && s.current[jobID].Running {
		return true
	}

	return checks.IsJobRunning(s.clientset, logger, job, func() *versions.Version {
		return s.kubernetesVersion
	})
}

func (s *JobScheduler) handleInProgress(logger logr.Logger, jobID string, job *batchv1.Job) {
	if s.isJobRunning(logger, jobID, job) {
		s.current[jobID].Running = true
		logger.Info("Job is running", "for", time.Since(job.Status.StartTime.Time))
		return
	}

	waitingFor := time.Since(job.CreationTimestamp.Time)
	if waitingFor > s.config.JobStartTimeout {
		logger.Error(nil, "job did not start in time - canceling", "status", job.Status, "for", waitingFor)
		delete(s.current, jobID)
		if err := s.delete(jobID); err != nil {
			logger.Error(err, "Error deleting job")
		}

		return
	}

	logger.Info("Job is starting", "status", job.Status, "for", waitingFor)
}

func (s *JobScheduler) handleSuccessfulJob(logger logr.Logger, jobID string, job *batchv1.Job) {
	logger.Info("Job finished successfully")

	// We remove it from the list of currently running jobs,
	// before we even check if the job should be deleted or not,
	// to make room for new jobs.
	delete(s.current, jobID)

	shouldDelete, err := s.ShouldDeleteJob(logger, s.config.KeepSuccessfulJobsFor, job.Status.CompletionTime.Time)
	if err != nil {
		logger.Error(err, "not able to determine if job is deletable - keeping job")
		return
	}

	if shouldDelete {
		logger.Info("Deleting job")
		if err := s.delete(jobID); err != nil {
			logger.Error(err, "Error deleting job")
			return
		}
	}
}

func (s *JobScheduler) handleFailedJob(logger logr.Logger, jobID string, job *batchv1.Job) {
	logger.Info("Job failed", "reason", getFailedReason(job), "message", getFailedMessage(job))

	// We remove it from the list of currently running jobs,
	// before we even check if the job should be deleted or not,
	// to make room for new jobs.
	delete(s.current, jobID)

	shouldDelete, err := s.ShouldDeleteJob(logger, s.config.KeepFailedJobsFor, job.CreationTimestamp.Time)
	if err != nil {
		logger.Error(err, "not able to determine current number of failed jobs - not deleting")
		return
	}

	if shouldDelete {
		logger.Info("Deleting job")
		if err := s.delete(jobID); err != nil {
			logger.Error(err, "Error deleting job")
			return
		}
	}
}

func (s *JobScheduler) ShouldDeleteJob(l logr.Logger, keepFor time.Duration, t time.Time) (bool, error) {
	if keepFor == 0 {
		l.Info("No retention policy set - job should be deleted")
		return true, nil
	}

	since := time.Since(t)
	if since > keepFor {
		l.Info("Retention policy reached - job should be deleted", "policy", keepFor, "elapsed", since)
		return true, nil
	}

	l.Info("Retention policy not reached - job should be kept", "policy", keepFor, "elapsed", since)
	return false, nil
}

func (s *JobScheduler) OnDelete(obj interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job := obj.(*batchv1.Job)
	jobID, ok := job.Labels[config.JobIDLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.JobIDLabel)
		return
	}

	agentType, ok := job.Labels[config.AgentTypeLabel]
	if !ok {
		klog.Warningf("Job '%s' is missing '%s' label", job.Name, config.AgentTypeLabel)
		return
	}

	delete(s.current, jobID)
	klog.InfoS("Job deleted", "job", jobID, "type", agentType)
}

func (s *JobScheduler) IsCurrentJob(jobID string) bool {
	_, ok := s.current[jobID]
	return ok
}

func getFailedMessage(job *batchv1.Job) string {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed {
			return cond.Message
		}
	}

	return ""
}

func getFailedReason(job *batchv1.Job) string {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed {
			return cond.Reason
		}
	}

	return ""
}

func jobConditions(job *batchv1.Job) []batchv1.JobConditionType {
	jobConditions := []batchv1.JobConditionType{}
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		jobConditions = append(jobConditions, cond.Type)
	}

	return jobConditions
}
