package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/renderedtext/agent-k8s-stack/pkg/config"
	"github.com/renderedtext/agent-k8s-stack/pkg/controller"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"github.com/renderedtext/agent-k8s-stack/pkg/signals"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

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

	cfg, err := config.NewConfigFromEnv(endpoint)
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
	informerFactory, err := NewInformerFactory(clientset, cfg)
	if err != nil {
		klog.Errorf("error creating informer factory: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	controller, err := controller.New(
		ctx,
		informerFactory,
		cfg,
		semaphoreClient,
		clientset,
	)

	if err != nil {
		klog.Errorf("error creating controller: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	informerFactory.Start(ctx.Done())

	if err = controller.Run(ctx); err != nil {
		klog.Errorf("Error running controller: %v", err)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
}

// Returns an informer factory configured to watch resources
// (secrets, pods, jobs) labeled with a semaphoreci.com/resource-type label.
func NewInformerFactory(clientset kubernetes.Interface, cfg *config.Config) (informers.SharedInformerFactory, error) {
	requirements := []labels.Requirement{}
	hasResourceType, err := labels.NewRequirement(config.ResourceTypeLabel, selection.Exists, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resource type requirement: %w", err)
	}

	requirements = append(requirements, *hasResourceType)
	return informers.NewSharedInformerFactoryWithOptions(
		clientset,
		time.Minute,
		informers.WithNamespace(cfg.Namespace),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = labels.NewSelector().Add(requirements...).String()
		}),
	), nil
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
