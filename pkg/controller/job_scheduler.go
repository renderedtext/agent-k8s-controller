package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
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

type JobScheduler struct {
	clientset kubernetes.Interface
	config    *config.Config
	current   map[string]semaphore.JobRequest
	mu        sync.Mutex
}

func NewJobScheduler(clientset kubernetes.Interface, config *config.Config) *JobScheduler {
	return &JobScheduler{
		current:   map[string]semaphore.JobRequest{},
		clientset: clientset,
		config:    config,
	}
}

func (s *JobScheduler) RegisterInformer(informerFactory informers.SharedInformerFactory) error {
	informer := informerFactory.Batch().V1().Jobs()
	_, err := informer.Informer().AddEventHandler(s)
	return err
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
		s.current[req.JobID] = req
		klog.LoggerWithValues(klog.Background(), "jobID", req.JobID, "agentType", req.MachineType).Info("Job created")
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
		s.current[jobID] = semaphore.JobRequest{JobID: jobID, MachineType: agentType}
		klog.LoggerWithValues(klog.Background(), "jobID", jobID, "agentType", agentType).Info("Job loaded")
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

	logger := klog.LoggerWithValues(klog.Background(), "jobID", jobID, "agentType", agentType)

	switch jobState(job) {
	case string(batchv1.JobComplete):
		s.handleSuccessfulJob(logger, jobID, job)

	case string(batchv1.JobFailed):
		s.handleFailedJob(logger, jobID, job)

	default:
		logger.Info("Job not yet finished")
	}
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

	klog.
		LoggerWithValues(klog.Background(), "jobID", jobID, "agentType", agentType).
		Info("Job deleted")
}

func (s *JobScheduler) JobExists(jobID string) bool {
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

func jobState(job *batchv1.Job) string {
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}

		return string(cond.Type)
	}

	return ""
}
