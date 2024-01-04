package controller

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/dgraph-io/ristretto"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

type AgentType struct {
	SecretName        string
	AgentTypeName     string
	RegistrationToken string
	PodSpecConfigMap  string
}

// We cache the agent type secret information
// to avoid going to the Kubernetes on every iteration.
// But we should also reach to changes to the agent type secrets,
// so we put an expiration on them.
var SecretCacheTTL = 5 * time.Minute

type AgentTypeFinder struct {
	secretsInterface typedv1.SecretInterface
	cache            *ristretto.Cache
}

func NewAgentTypeFinder(client kubernetes.Interface, namespace string) (*AgentTypeFinder, error) {
	// The provider needs read access to secrets,
	// so it can find all the secrets for each agent type,
	// and use the agent type token in them to grab metrics from the Semaphore API.
	secretsInterface := client.CoreV1().Secrets(namespace)

	/*
	 * We keep at most 50 agent types in our cache.
	 */
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 500,
		MaxCost:     50,
		BufferItems: 64,
		Metrics:     false,
	})

	if err != nil {
		return nil, err
	}

	return &AgentTypeFinder{
		secretsInterface: secretsInterface,
		cache:            cache,
	}, nil
}

func (f *AgentTypeFinder) Find() ([]*AgentType, error) {
	list, err := f.secretsInterface.List(context.Background(), metav1.ListOptions{
		LabelSelector: "semaphoreci.com/resource-type=agent-type-configuration",
	})

	if err != nil {
		return []*AgentType{}, fmt.Errorf("error listing secrets: %v", err)
	}

	agentTypes := []*AgentType{}
	for _, secret := range list.Items {
		agentType, err := f.findAgentType(secret.GetName())
		if err != nil {
			log.Printf("Error decoding secret %s: %v", secret.GetName(), err)
			continue
		}

		agentTypes = append(agentTypes, agentType)
	}

	return agentTypes, nil
}

// Get the agent type information (endpoint and token) from the secret specified.
// We also cache this information to avoid going to the Kubernetes API on every iteration.
func (f *AgentTypeFinder) findAgentType(secretName string) (*AgentType, error) {
	value, found := f.cache.Get(secretName)
	if found {
		if info, ok := value.(*AgentType); ok {
			return info, nil
		}
	}

	// If the agent type info does not exist in the cache,
	// we fetch the information from the Kubernetes API.
	o, err := f.secretsInterface.Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error describing secret: %v", err)
	}

	info, err := f.secretToAgentType(o)
	if err != nil {
		return nil, fmt.Errorf("error finding agent type information in secret: %v", err)
	}

	f.cache.SetWithTTL(secretName, info, 1, SecretCacheTTL)
	return info, nil
}

func (f *AgentTypeFinder) secretToAgentType(secret *v1.Secret) (*AgentType, error) {
	agentTypeName, ok := secret.Data["agentTypeName"]
	if !ok {
		return nil, fmt.Errorf("no agentTypeName field in secret '%s'", secret.GetName())
	}

	registrationToken, ok := secret.Data["registrationToken"]
	if !ok {
		return nil, fmt.Errorf("no registrationToken field in secret '%s'", secret.GetName())
	}

	podSpecConfigMap, ok := secret.Data["podSpecConfigMap"]
	if !ok {
		podSpecConfigMap = []byte{}
	}

	return &AgentType{
		SecretName:        secret.GetName(),
		AgentTypeName:     string(agentTypeName),
		RegistrationToken: string(registrationToken),
		PodSpecConfigMap:  string(podSpecConfigMap),
	}, nil
}
