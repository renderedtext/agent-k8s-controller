package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/util/wait"

	agentTypes "github.com/renderedtext/agent-k8s-stack/pkg/agent_types"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Namespace              string
	ServiceAccountName     string
	AgentImage             string
	AgentStartupParameters []string
	Labels                 []string
	MaxParallelJobs        int
	SemaphoreEndpoint      string
}

type Controller struct {
	cfg               *Config
	semaphoreClient   *semaphore.Client
	agentTypeRegistry *agentTypes.Registry
	clientset         kubernetes.Interface

	currentJobs []semaphore.JobRequest
}

func New(
	ctx context.Context,
	informerFactory informers.SharedInformerFactory,
	cfg *Config,
	semaphoreClient *semaphore.Client,
	clientset kubernetes.Interface) (*Controller, error) {

	agentTypeRegistry, err := agentTypes.NewRegistry()
	if err != nil {
		return nil, err
	}

	if err := agentTypeRegistry.RegisterInformer(informerFactory); err != nil {
		return nil, err
	}

	return &Controller{
		cfg:               cfg,
		semaphoreClient:   semaphoreClient,
		clientset:         clientset,
		agentTypeRegistry: agentTypeRegistry,
		currentJobs:       []semaphore.JobRequest{},
	}, nil
}

func (c *Controller) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting controller")

	go wait.UntilWithContext(ctx, c.runWorker, time.Second)

	<-ctx.Done()
	logger.Info("Shutting down workers")

	return nil
}

func (c *Controller) runWorker(ctx context.Context) {
	for c.tick(ctx) {
		time.Sleep(10 * time.Second)
	}
}

func (c *Controller) tick(ctx context.Context) bool {
	agentTypes := c.agentTypeRegistry.All()
	if len(agentTypes) == 0 {
		klog.Info("No agent types found")
		return true
	}

	klog.Infof("Polling job queue for %v", agentTypes)
	jobs, err := c.semaphoreClient.JobsFor(agentTypes)
	if err != nil {
		klog.Error(err, "error polling job queue")
		return true
	}

	klog.Infof("Found %d jobs in the queue", len(jobs))
	for _, j := range jobs {
		if len(c.currentJobs) == c.cfg.MaxParallelJobs {
			klog.Infof("Reached %d max parallel number of jobs", c.cfg.MaxParallelJobs)
			break
		}

		c.addJob(j)
	}

	c.reconcile(ctx)
	return true
}

func (c *Controller) reconcile(ctx context.Context) {
	for _, j := range c.currentJobs {
		c.reconcileJob(ctx, j)
	}
}

func (c *Controller) jobName(jobID string) string {
	return fmt.Sprintf("semaphore-agent-%s", jobID)
}

func (c *Controller) reconcileJob(ctx context.Context, job semaphore.JobRequest) {
	logger := klog.LoggerWithValues(klog.Background(), "jobID", job.JobID, "agentType", job.MachineType)
	j, err := c.clientset.BatchV1().
		Jobs(c.cfg.Namespace).
		Get(ctx, c.jobName(job.JobID), metav1.GetOptions{})

	// Job already exists, check if it's finished
	if err == nil {
		if j.Status.Succeeded > 0 {
			logger.Info("Job finished successfully - deleting")
			if err := c.deleteJob(ctx, c.clientset, j.Name); err != nil {
				logger.Error(err, "Error deleting finished job")
			}

			c.removeJob(job.JobID)
			return
		}

		// NOTE: if the job finished, but failed, we leave it around for troubleshooting purposes.
		if j.Status.Failed > 0 {
			logger.Info("Job finished and failed")
			return
		}

		logger.Info("Job not yet finished")
	}

	// Job does not exist, so we need to create it.
	if errors.IsNotFound(err) {
		err := c.createJob(ctx, c.clientset, job)
		if err != nil {
			logger.Error(err, "Error creating job")
		}

		logger.Info("Job created successfully")
		return
	}

	if err != nil {
		logger.Error(err, "Unknown error when trying to find job")
	}
}

func (c *Controller) exists(ID string) bool {
	for _, j := range c.currentJobs {
		if j.JobID == ID {
			return true
		}
	}

	return false
}

func (c *Controller) addJob(job semaphore.JobRequest) {
	if !c.exists(job.JobID) {
		c.currentJobs = append(c.currentJobs, job)
	}
}

func (c *Controller) removeJob(ID string) {
	newJobs := []semaphore.JobRequest{}
	for _, j := range c.currentJobs {
		if j.JobID != ID {
			newJobs = append(newJobs, j)
		}
	}

	c.currentJobs = newJobs
}

func (c *Controller) createJob(ctx context.Context, k8sClient kubernetes.Interface, job semaphore.JobRequest) error {
	k8sJob, err := c.buildJob(job)
	if err != nil {
		return err
	}

	_, err = k8sClient.BatchV1().
		Jobs(c.cfg.Namespace).
		Create(ctx, k8sJob, metav1.CreateOptions{})
	return err
}

func (c *Controller) deleteJob(ctx context.Context, k8sClient kubernetes.Interface, name string) error {
	propagationPolicy := metav1.DeletePropagationBackground
	return k8sClient.BatchV1().
		Jobs(c.cfg.Namespace).
		Delete(ctx, name, metav1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
}

func (c *Controller) buildJob(job semaphore.JobRequest) (*batchv1.Job, error) {
	agentType := c.agentTypeRegistry.Get(job.MachineType)
	if agentType == nil {
		return nil, fmt.Errorf("nil agent type")
	}

	parallelism := int32(1)
	retries := int32(0)
	activeDeadlineSeconds := int64(60 * 60 * 24) // 1 day
	terminationGracePeriod := int64(300)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.jobName(job.JobID),
			Namespace: c.cfg.Namespace,
			Labels:    c.buildLabels(job),
		},
		Spec: batchv1.JobSpec{
			Parallelism:           &parallelism,
			Completions:           &parallelism,
			BackoffLimit:          &retries,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: c.buildLabels(job)},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					ServiceAccountName:            c.cfg.ServiceAccountName,
					TerminationGracePeriodSeconds: &terminationGracePeriod,
					Containers: []corev1.Container{
						{
							Name:  "semaphore-agent",
							Image: c.cfg.AgentImage,
							Command: []string{
								"/opt/semaphore/agent",
								"start",
							},
							Args: c.buildAgentStartupParameters(agentType, job.JobID),
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
	}, nil
}

func (c *Controller) buildLabels(job semaphore.JobRequest) map[string]string {
	labels := map[string]string{
		"semaphoreci.com/agent-type": job.MachineType,
	}

	for _, label := range c.cfg.Labels {
		parts := strings.Split(label, "=")
		labels[parts[0]] = parts[1]
	}

	return labels
}

func (c *Controller) buildAgentStartupParameters(agentType *agentTypes.AgentType, jobID string) []string {
	labels := []string{
		fmt.Sprintf("semaphoreci.com/agent-type=%s", agentType.AgentTypeName),
	}

	if len(c.cfg.Labels) > 0 {
		labels = append(labels, c.cfg.Labels...)
	}

	parameters := []string{
		"--endpoint",
		c.cfg.SemaphoreEndpoint,
		"--job-id",
		jobID,
		"--kubernetes-labels",
		strings.Join(labels, ","),
		"--kubernetes-executor",
		"--disconnect-after-job",
	}

	// If agent type does not specify startup parameters, use the controller's defaults.
	if len(agentType.AgentStartupParameters) == 0 {
		return append(parameters, c.cfg.AgentStartupParameters...)
	}

	// Otherwise, use the agent type's startup parameters.
	return append(parameters, agentType.AgentStartupParameters...)
}
