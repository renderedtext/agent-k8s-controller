package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/renderedtext/agent-k8s-stack/pkg/controller"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	apiToken := os.Getenv("SEMAPHORE_API_TOKEN")
	if apiToken == "" {
		log.Fatal("no SEMAPHORE_API_TOKEN specified")
	}

	endpoint := os.Getenv("SEMAPHORE_ENDPOINT")
	if endpoint == "" {
		log.Fatal("no SEMAPHORE_ENDPOINT specified")
	}

	cfg, err := buildConfig(endpoint)
	if err != nil {
		log.Fatalf("could not build config: %v", err)
	}

	clientset, err := newK8sClientset()
	if err != nil {
		log.Fatal("error creating k8s clientset")
	}

	semaphoreClient := semaphore.NewClient(endpoint, apiToken)
	controller, err := controller.New(cfg, semaphoreClient, clientset)
	if err != nil {
		panic(err)
	}

	controller.Run()
}

func buildConfig(endpoint string) (*controller.Config, error) {
	k8sNamespace := os.Getenv("KUBERNETES_NAMESPACE")
	if k8sNamespace == "" {
		return nil, fmt.Errorf("no KUBERNETES_NAMESPACE specified")
	}

	svcAccountName := os.Getenv("KUBERNETES_SERVICE_ACCOUNT")
	if svcAccountName == "" {
		return nil, fmt.Errorf("no KUBERNETES_SERVICE_ACCOUNT specified")
	}

	agentImage := os.Getenv("SEMAPHORE_AGENT_IMAGE")
	if agentImage == "" {
		return nil, fmt.Errorf("no SEMAPHORE_AGENT_IMAGE specified")
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

	return &controller.Config{
		SemaphoreEndpoint:  endpoint,
		Namespace:          k8sNamespace,
		ServiceAccountName: svcAccountName,
		AgentImage:         agentImage,
		MaxParallelJobs:    maxParallelJobs,
	}, nil
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
