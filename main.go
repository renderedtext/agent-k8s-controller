package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Controller struct {
	cfg       *Config
	api       *API
	clientset kubernetes.Interface

	currentJobs []string
}

func NewController(cfg *Config, api *API, clientset kubernetes.Interface) *Controller {
	return &Controller{
		cfg:         cfg,
		api:         api,
		clientset:   clientset,
		currentJobs: []string{},
	}
}

func (c *Controller) Run() {
	for {
		c.tick()
		time.Sleep(10 * time.Second)
	}
}

func (c *Controller) tick() {
	log.Printf("Looking for jobs...\n")
	jobs, err := c.api.JobsFor(c.cfg.MachineType)
	if err != nil {
		log.Printf("Error: %v\n", err)
	}

	log.Printf("Found jobs: %v\n", jobs)
	for _, j := range jobs {
		if len(c.currentJobs) == c.cfg.MaxParallelJobs {
			log.Printf("Reached max parallel number of jobs: %d\n", c.cfg.MaxParallelJobs)
			break
		}

		c.addJob(j)
	}

	c.reconcile()
}

func (c *Controller) reconcile() {
	log.Printf("Current jobs: %v\n", c.currentJobs)
	for _, j := range c.currentJobs {
		log.Printf("Reconciling job %s\n", j)
		c.reconcileJob(j)
	}
}

func (c *Controller) reconcileJob(ID string) {
	jobs, err := c.clientset.BatchV1().
		Jobs(c.cfg.Namespace).
		List(context.Background(), v1.ListOptions{
			LabelSelector: fmt.Sprintf("app=semaphore,id=%s", ID),
		})

	if err != nil {
		log.Printf("[%s] Unknown error when trying to find job: %v\n", ID, err)
		return
	}

	if len(jobs.Items) > 1 {
		log.Printf("[%s] This should never happen\n", ID)
		return
	}

	// job does not exist yet, create it
	if len(jobs.Items) == 0 {
		err := createJob(c.clientset, ID, c.cfg)
		if err != nil {
			log.Printf("[%s] Error creating job: %v\n", err, ID)
		} else {
			log.Printf("[%s] Job created successfully\n", ID)
		}

		return
	}

	j := jobs.Items[0]
	if j.Status.Succeeded > 0 {
		log.Printf("[%s] Job finished successfully - deleting...\n", ID)
		if err := deleteJob(c.clientset, j.Name, j.Namespace); err != nil {
			log.Printf("[%s] Error deleting finished job - %v\n", ID, err)
		} else {
			c.removeJob(ID)
		}

		return
	}

	// NOTE: if the job finished, but failed, we leave it around for troubleshooting purposes.
	if j.Status.Failed > 0 {
		log.Printf("[%s] Job finished and failed\n", ID)
		return
	}

	log.Printf("[%s] Job not yet finished.\n", ID)
}

func (c *Controller) exists(ID string) bool {
	for _, j := range c.currentJobs {
		if j == ID {
			return true
		}
	}

	return false
}

func (c *Controller) addJob(ID string) {
	if !c.exists(ID) {
		c.currentJobs = append(c.currentJobs, ID)
	}
}

func (c *Controller) removeJob(ID string) {
	newJobs := []string{}
	for _, j := range c.currentJobs {
		if j != ID {
			newJobs = append(newJobs, j)
		}
	}

	c.currentJobs = newJobs
}

type Config struct {
	MachineType           string
	Namespace             string
	ServiceAccountName    string
	AgentImage            string
	AgentConfigFromSecret string
	MaxParallelJobs       int
}

func main() {
	apiToken := os.Getenv("SEMAPHORE_AGENT_API_TOKEN")
	if apiToken == "" {
		log.Fatal("no SEMAPHORE_AGENT_API_TOKEN specified")
	}

	endpoint := os.Getenv("SEMAPHORE_ENDPOINT")
	if endpoint == "" {
		log.Fatal("no SEMAPHORE_ENDPOINT specified")
	}

	cfg, err := buildConfig()
	if err != nil {
		log.Fatalf("could not build config: %v", err)
	}

	clientset, err := newK8sClientset()
	if err != nil {
		log.Fatal("error creating k8s clientset")
	}

	api := NewAPI(endpoint, apiToken)
	controller := NewController(cfg, api, clientset)
	controller.Run()
}

func buildConfig() (*Config, error) {
	k8sNamespace := os.Getenv("KUBERNETES_NAMESPACE")
	if k8sNamespace == "" {
		return nil, fmt.Errorf("no KUBERNETES_NAMESPACE specified")
	}

	agentConfigFromSecret := os.Getenv("SEMAPHORE_AGENT_CONFIG_FROM_SECRET")
	if agentConfigFromSecret == "" {
		return nil, fmt.Errorf("no SEMAPHORE_AGENT_CONFIG_FROM_SECRET specified")
	}

	svcAccountName := os.Getenv("KUBERNETES_SERVICE_ACCOUNT")
	if svcAccountName == "" {
		return nil, fmt.Errorf("no KUBERNETES_SERVICE_ACCOUNT specified")
	}

	machineType := os.Getenv("SEMAPHORE_AGENT_MACHINE_TYPE")
	if machineType == "" {
		return nil, fmt.Errorf("no SEMAPHORE_AGENT_MACHINE_TYPE specified")
	}

	agentImage := "semaphoreci/agent:v2.2.13"
	if os.Getenv("SEMAPHORE_AGENT_IMAGE") != "" {
		agentImage = os.Getenv("SEMAPHORE_AGENT_IMAGE")
	}

	maxParallelJobs := 10
	if os.Getenv("MAX_PARALLEL_JOBS") != "" {
		fromEnv := os.Getenv("MAX_PARALLEL_JOBS")
		v, err := strconv.Atoi(fromEnv)
		if err != nil {
			log.Printf("Error parsing MAX_PARALLEL_JOBS (%s): %v - using default", fromEnv, err)
			maxParallelJobs = 10
		} else {
			maxParallelJobs = v
		}
	}

	return &Config{
		Namespace:             k8sNamespace,
		ServiceAccountName:    svcAccountName,
		AgentImage:            agentImage,
		MachineType:           machineType,
		AgentConfigFromSecret: agentConfigFromSecret,
		MaxParallelJobs:       maxParallelJobs,
	}, nil
}

func createJob(k8sClient kubernetes.Interface, semaphoreJobID string, cfg *Config) error {
	k8sJobName := fmt.Sprintf("semaphore-agent-%d", time.Now().UnixNano())
	log.Printf("Creating job to run agent %s\n", k8sJobName)
	_, err := k8sClient.BatchV1().
		Jobs(cfg.Namespace).
		Create(context.Background(), buildJob(k8sJobName, semaphoreJobID, cfg), v1.CreateOptions{})
	return err
}

func deleteJob(k8sClient kubernetes.Interface, name, namespace string) error {
	propagationPolicy := v1.DeletePropagationBackground
	return k8sClient.BatchV1().
		Jobs(namespace).
		Delete(context.Background(), name, v1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
}

func buildJob(k8sJobName, semaphoreJobID string, cfg *Config) *batchv1.Job {
	parallelism := int32(1)
	retries := int32(0)
	activeDeadlineSeconds := int64(60 * 60 * 24) // 1 day
	terminationGracePeriod := int64(300)

	return &batchv1.Job{
		ObjectMeta: v1.ObjectMeta{
			Name:      k8sJobName,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app": "semaphore",
				"id":  semaphoreJobID,
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
					ServiceAccountName:            cfg.ServiceAccountName,
					TerminationGracePeriodSeconds: &terminationGracePeriod,
					Volumes: []corev1.Volume{
						{
							Name: "agent-config-volume",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cfg.AgentConfigFromSecret,
									Items: []corev1.KeyToPath{
										{
											Key:  "semaphore-agent.yml",
											Path: "semaphore-agent.yml",
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "semaphore-agent",
							Image: cfg.AgentImage,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "agent-config-volume",
									MountPath: "/opt/semaphore/semaphore-agent.yml",
									ReadOnly:  true,
									SubPath:   "semaphore-agent.yml",
								},
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
	}
}

func newK8sClientset() (kubernetes.Interface, error) {
	clientset, err := newInClusterClientset()
	if err != nil {
		log.Printf("No in-cluster configuration found - using ~/.kube/config...\n")

		clientset, err = newClientsetFromConfig()
		if err != nil {
			return nil, fmt.Errorf("error creating kubernetes clientset: %v\n", err)
		}
	}

	return clientset, nil
}

func newClientsetFromConfig() (kubernetes.Interface, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("error getting user home directory: %v\n", err)
	}

	kubeConfigPath := filepath.Join(homeDir, ".kube", "config")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error getting Kubernetes config: %v\n", err)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating kubernetes clientset from config file: %v\n", err)
	}

	return clientset, nil
}

func newInClusterClientset() (kubernetes.Interface, error) {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	return clientset, nil
}

type API struct {
	Endpoint string
	Token    string
}

func NewAPI(endpoint, token string) *API {
	return &API{
		Endpoint: endpoint,
		Token:    token,
	}
}

type APIResponse struct {
	Jobs []APIJob `json:"jobs" yaml:"jobs"`
}

type APIJob struct {
	Metadata APIJobMetadata `json:"metadata" yaml:"metadata"`
	Spec     APIJobSpec     `json:"spec" yaml:"spec"`
}

// There are more fields here, but I only care about the ID for now.
type APIJobMetadata struct {
	ID string `json:"id" yaml:"id"`
}

// There are more fields here, but I only care about the machine type for now.
type APIJobSpec struct {
	Agent struct {
		Machine struct {
			Type string
		}
	}
}

// To find the jobs in the queue, we use the public Semaphore API,
// not the /api/v1/self_hosted_agents/metrics one.
func (a *API) JobsFor(machineType string) ([]string, error) {
	URL := fmt.Sprintf("https://%s/api/v1alpha/jobs?states=PENDING&states=QUEUED", a.Endpoint)
	req, err := http.NewRequest(http.MethodGet, URL, nil)
	if err != nil {
		return []string{}, err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Token %s", a.Token))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return []string{}, err
	}

	apiResponse := APIResponse{}
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return []string{}, err
	}

	js := []string{}
	for _, j := range apiResponse.Jobs {
		if j.Spec.Agent.Machine.Type == machineType {
			js = append(js, j.Metadata.ID)
		}
	}

	return js, nil
}
