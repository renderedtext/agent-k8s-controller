package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	AgentTypeLabel           = "semaphoreci.com/agent-type"
	JobIDLabel               = "semaphoreci.com/job-id"
	ResourceTypeLabel        = "semaphoreci.com/resource-type"
	AgentTypeResourceType    = "agent-type-configuration"
	SemaphoreJobResourceType = "semaphore-job"
)

type Config struct {
	Namespace              string
	ServiceAccountName     string
	AgentImage             string
	AgentStartupParameters []string
	Labels                 []string
	MaxParallelJobs        int
	SemaphoreEndpoint      string
	KeepFailedJobsFor      time.Duration
	KeepSuccessfulJobsFor  time.Duration
}

func NewConfigFromEnv(endpoint string) (*Config, error) {
	k8sNamespace := os.Getenv("KUBERNETES_NAMESPACE")
	if k8sNamespace == "" {
		k8sNamespace = "default"
		klog.Warningf("no KUBERNETES_NAMESPACE specified - using '%s'", k8sNamespace)
	}

	agentImage := os.Getenv("SEMAPHORE_AGENT_IMAGE")
	if agentImage == "" {
		agentImage = "semaphoreci/agent:latest"
		klog.Warningf("no SEMAPHORE_AGENT_IMAGE specified - using '%s'", agentImage)
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

	agentStartupParameters := []string{}
	if os.Getenv("SEMAPHORE_AGENT_STARTUP_PARAMETERS") != "" {
		agentStartupParameters = strings.Split(os.Getenv("SEMAPHORE_AGENT_STARTUP_PARAMETERS"), " ")
	}

	labels, err := parseLabels()
	if err != nil {
		return nil, fmt.Errorf("unable to determine labels")
	}

	return &Config{
		SemaphoreEndpoint:      endpoint,
		Namespace:              k8sNamespace,
		ServiceAccountName:     os.Getenv("KUBERNETES_SERVICE_ACCOUNT"),
		AgentImage:             agentImage,
		AgentStartupParameters: agentStartupParameters,
		MaxParallelJobs:        maxParallelJobs,
		Labels:                 labels,
		KeepFailedJobsFor:      keepFailedJobsFor(),
		KeepSuccessfulJobsFor:  keepSuccessfulJobsFor(),
	}, nil
}

func parseLabels() ([]string, error) {
	labels := []string{}
	fromEnv := os.Getenv("SEMAPHORE_AGENT_LABELS")
	if fromEnv == "" {
		return labels, nil
	}

	for _, label := range strings.Split(fromEnv, ",") {
		parts := strings.Split(label, "=")
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s is not a valid label", label)
		}

		labels = append(labels, label)
	}

	return labels, nil
}

func keepFailedJobsFor() time.Duration {
	keepFor, err := time.ParseDuration(os.Getenv("KEEP_FAILED_JOBS_FOR"))
	if err != nil {
		return 0
	}

	return keepFor
}

func keepSuccessfulJobsFor() time.Duration {
	keepFor, err := time.ParseDuration(os.Getenv("KEEP_SUCCESSFUL_JOBS_FOR"))
	if err != nil {
		return 0
	}

	return keepFor
}
