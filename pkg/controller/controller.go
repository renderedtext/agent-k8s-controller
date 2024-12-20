package controller

import (
	"context"
	"errors"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
	"github.com/renderedtext/agent-k8s-stack/pkg/config"
	"github.com/renderedtext/agent-k8s-stack/pkg/semaphore"
	"k8s.io/client-go/kubernetes"
)

type Controller struct {
	semaphoreClient   *semaphore.Client
	agentTypeRegistry *agenttypes.Registry
	clientset         kubernetes.Interface
	jobScheduler      *JobScheduler
}

func New(
	ctx context.Context,
	informerFactory informers.SharedInformerFactory,
	config *config.Config,
	semaphoreClient *semaphore.Client,
	clientset kubernetes.Interface) (*Controller, error) {

	agentTypeRegistry, err := agenttypes.NewRegistry()
	if err != nil {
		return nil, err
	}

	if err := agentTypeRegistry.RegisterInformer(informerFactory); err != nil {
		return nil, err
	}

	jobScheduler, err := NewJobScheduler(clientset, config)
	if err != nil {
		return nil, err
	}

	if err := jobScheduler.RegisterInformer(informerFactory); err != nil {
		return nil, err
	}

	return &Controller{
		semaphoreClient:   semaphoreClient,
		clientset:         clientset,
		agentTypeRegistry: agentTypeRegistry,
		jobScheduler:      jobScheduler,
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
		klog.Info("Not polling Semaphore API - no agent types found")
		return true
	}

	if !c.jobScheduler.HasSpace() {
		klog.Info("Not polling Semaphore API - no space")
		return true
	}

	klog.InfoS("Polling Semaphore API", "types", agentTypeNames(agentTypes))
	jobs, err := c.semaphoreClient.ListJobs(agentTypes)
	if err != nil {
		klog.Error(err, "error polling job queue")
		return true
	}

	klog.Infof("Found %d jobs in the queue", len(jobs))

	for _, j := range jobs {
		agentType := c.agentTypeRegistry.Get(j.MachineType)
		if agentType == nil {
			klog.Warningf("agent type '%s' not registered", j.MachineType)
			continue
		}

		err := c.jobScheduler.Create(ctx, j, agentType)
		if errors.Is(err, ErrParallelJobsLimitReached) {
			klog.Info("Reached limit of parallel jobs")
			break
		}
	}

	return true
}

func agentTypeNames(agentTypes []*agenttypes.AgentType) []string {
	names := []string{}
	for _, agentType := range agentTypes {
		names = append(names, agentType.AgentTypeName)
	}
	return names
}
