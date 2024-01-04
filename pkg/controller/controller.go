package controller

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Config struct {
	Namespace          string
	ServiceAccountName string
	AgentImage         string
	MaxParallelJobs    int
	SemaphoreEndpoint  string
}

type Controller struct {
	cfg             *Config
	semaphoreClient *semaphore.Client
	agentTypeFinder *AgentTypeFinder
	clientset       kubernetes.Interface

	currentJobs []semaphore.JobRequest
}

func New(cfg *Config, semaphoreClient *semaphore.Client, clientset kubernetes.Interface) (*Controller, error) {
	agentTypeFinder, err := NewAgentTypeFinder(clientset, cfg.Namespace)
	if err != nil {
		return nil, err
	}

	return &Controller{
		cfg:             cfg,
		semaphoreClient: semaphoreClient,
		clientset:       clientset,
		agentTypeFinder: agentTypeFinder,
		currentJobs:     []semaphore.JobRequest{},
	}, nil
}

func (c *Controller) Run() {
	for {
		c.tick()
		time.Sleep(10 * time.Second)
	}
}

func (c *Controller) tick() {
	agentTypes, err := c.agentTypeFinder.Find()
	if err != nil {
		log.Printf("Error finding agent types: %v", err)
	}

	log.Printf("Looking for jobs...\n")
	jobs, err := c.semaphoreClient.JobsFor(agentTypeNames(agentTypes))
	if err != nil {
		log.Printf("Error: %v\n", err)
		return
	}

	log.Printf("Found %d jobs in the queue\n", len(jobs))
	for _, j := range jobs {
		if len(c.currentJobs) == c.cfg.MaxParallelJobs {
			log.Printf("Reached max parallel number of jobs: %d\n", c.cfg.MaxParallelJobs)
			break
		}

		c.addJob(j)
	}

	c.reconcile(agentTypes)
}

func agentTypeNames(agentTypes []*AgentType) []string {
	names := []string{}
	for _, agentType := range agentTypes {
		names = append(names, agentType.AgentTypeName)
	}

	return names
}

func (c *Controller) reconcile(agentTypes []*AgentType) {
	log.Printf("Current jobs: %v\n", c.currentJobs)
	for _, j := range c.currentJobs {
		log.Printf("Reconciling job %s\n", j)
		c.reconcileJob(j, agentTypes)
	}
}

func (c *Controller) reconcileJob(job semaphore.JobRequest, agentTypes []*AgentType) {
	jobs, err := c.clientset.BatchV1().
		Jobs(c.cfg.Namespace).
		List(context.Background(), v1.ListOptions{
			LabelSelector: fmt.Sprintf("app=semaphore,semaphoreci.com/job-id=%s", job.JobID),
		})

	if err != nil {
		log.Printf("[%s] Unknown error when trying to find job: %v\n", job.JobID, err)
		return
	}

	if len(jobs.Items) > 1 {
		log.Printf("[%s] This should never happen\n", job.JobID)
		return
	}

	// job does not exist yet, create it
	if len(jobs.Items) == 0 {
		err := c.createJob(c.clientset, job, agentTypes)
		if err != nil {
			log.Printf("[%s] Error creating job: %v\n", err, job.JobID)
		} else {
			log.Printf("[%s] Job created successfully\n", job.JobID)
		}

		return
	}

	j := jobs.Items[0]
	if j.Status.Succeeded > 0 {
		log.Printf("[%s] Job finished successfully - deleting...\n", job.JobID)
		if err := c.deleteJob(c.clientset, j.Name); err != nil {
			log.Printf("[%s] Error deleting finished job - %v\n", job.JobID, err)
		} else {
			c.removeJob(job.JobID)
		}

		return
	}

	// NOTE: if the job finished, but failed, we leave it around for troubleshooting purposes.
	if j.Status.Failed > 0 {
		log.Printf("[%s] Job finished and failed\n", job.JobID)
		return
	}

	log.Printf("[%s] Job not yet finished.\n", job.JobID)
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

func (c *Controller) createJob(k8sClient kubernetes.Interface, job semaphore.JobRequest, agentTypes []*AgentType) error {
	k8sJob, err := c.buildJob(job, agentTypes)
	if err != nil {
		return err
	}

	_, err = k8sClient.BatchV1().
		Jobs(c.cfg.Namespace).
		Create(context.Background(), k8sJob, v1.CreateOptions{})
	return err
}

func (c *Controller) deleteJob(k8sClient kubernetes.Interface, name string) error {
	propagationPolicy := v1.DeletePropagationBackground
	return k8sClient.BatchV1().
		Jobs(c.cfg.Namespace).
		Delete(context.Background(), name, v1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
}

func findAgentType(agentTypes []*AgentType, typeName string) *AgentType {
	for _, agentType := range agentTypes {
		if agentType.AgentTypeName == typeName {
			return agentType
		}
	}

	return nil
}

func (c *Controller) buildJob(job semaphore.JobRequest, agentTypes []*AgentType) (*batchv1.Job, error) {
	agentType := findAgentType(agentTypes, job.MachineType)
	if agentType == nil {
		return nil, fmt.Errorf("nil agent type")
	}

	parallelism := int32(1)
	retries := int32(0)
	activeDeadlineSeconds := int64(60 * 60 * 24) // 1 day
	terminationGracePeriod := int64(300)

	return &batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			Name:      fmt.Sprintf("semaphore-agent-%s", job.JobID),
			Namespace: c.cfg.Namespace,
			Labels: map[string]string{
				"app":                        "semaphore",
				"semaphoreci.com/agent-type": job.MachineType,
			},
		},
		Spec: batchv1.JobSpec{
			Parallelism:           &parallelism,
			Completions:           &parallelism,
			BackoffLimit:          &retries,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			// Selector: ???,
			// TTLSecondsAfterFinished: ???,
			Template: corev1.PodTemplateSpec{
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
							Args: []string{
								"--endpoint",
								c.cfg.SemaphoreEndpoint,
								// TODO: do not pass registration token in plain text like this, use environment variable
								"--token",
								agentType.RegistrationToken,
								"--name-from-env",
								"KUBERNETES_POD_NAME",
								"--job-id",
								job.JobID,
								"--kubernetes-executor",
								"--disconnect-after-job",
							},
							Env: []corev1.EnvVar{
								{
									Name: "KUBERNETES_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
									},
								},
								{
									Name: "KUBERNETES_POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
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