package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klog "k8s.io/klog/v2"

	"github.com/renderedtext/agent-k8s-stack/pkg/controller"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"github.com/renderedtext/agent-k8s-stack/pkg/signals"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	flag.Parse()

	// set up signals so we handle the shutdown signal gracefully
	ctx := signals.SetupSignalHandler()

	apiToken := os.Getenv("SEMAPHORE_API_TOKEN")
	if apiToken == "" {
		klog.Error("invalid configuration: no SEMAPHORE_API_TOKEN specified")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	endpoint := os.Getenv("SEMAPHORE_ENDPOINT")
	if endpoint == "" {
		klog.Error("invalid configuration: no SEMAPHORE_ENDPOINT specified")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	cfg, err := buildConfig(endpoint)
	if err != nil {
		klog.Errorf("error building config: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	clientset, err := newK8sClientset()
	if err != nil {
		klog.Errorf("error creating kube client: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	semaphoreClient := semaphore.NewClient(endpoint, apiToken)
	informerFactory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		time.Second*30,
		informers.WithNamespace(cfg.Namespace),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = "semaphoreci.com/resource-type=agent-type-configuration"
		}),
	)

	controller, err := controller.New(ctx, informerFactory, cfg, semaphoreClient, clientset)
	if err != nil {
		panic(err)
	}

	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	informerFactory.Start(ctx.Done())

	if err = controller.Run(ctx); err != nil {
		klog.Errorf("Error running controller: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
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

	agentStartupParameters := []string{}
	if os.Getenv("SEMAPHORE_AGENT_STARTUP_PARAMETERS") != "" {
		agentStartupParameters = strings.Split(os.Getenv("SEMAPHORE_AGENT_STARTUP_PARAMETERS"), " ")
	}

	labels, err := parseLabels()
	if err != nil {
		return nil, fmt.Errorf("unable to determine labels")
	}

	return &controller.Config{
		SemaphoreEndpoint:      endpoint,
		Namespace:              k8sNamespace,
		ServiceAccountName:     svcAccountName,
		AgentImage:             agentImage,
		AgentStartupParameters: agentStartupParameters,
		MaxParallelJobs:        maxParallelJobs,
		Labels:                 labels,
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

func newK8sClientset() (kubernetes.Interface, error) {
	clientset, err := newInClusterClientset()
	if err != nil {
		log.Printf("No in-cluster configuration found - using ~/.kube/config...\n")

		clientset, err = newClientsetFromConfig()
		if err != nil {
			return nil, fmt.Errorf("error creating kubernetes clientset: %v", err)
		}
	}

	return clientset, nil
}

func newClientsetFromConfig() (kubernetes.Interface, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("error getting user home directory: %v", err)
	}

	kubeConfigPath := filepath.Join(homeDir, ".kube", "config")
	kubeConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("error getting Kubernetes config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("error creating kubernetes clientset from config file: %v", err)
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
