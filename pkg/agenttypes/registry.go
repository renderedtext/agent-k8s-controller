package agenttypes

import (
	"fmt"
	"strings"
	"sync"

	"github.com/renderedtext/agent-k8s-stack/pkg/podspec"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
)

type AgentType struct {
	SecretName             string
	AgentTypeName          string
	RegistrationToken      string
	AgentStartupParameters []string
	AgentPodSpec           *podspec.Spec
}

// The informer writes agent types while the controller's
// polling loop reads them, so the map is guarded by mu.
type Registry struct {
	mu         sync.RWMutex
	agentTypes map[string]*AgentType
}

func NewRegistry() (*Registry, error) {
	return &Registry{
		agentTypes: map[string]*AgentType{},
	}, nil
}

func (r *Registry) RegisterInformer(informerFactory informers.SharedInformerFactory) error {
	informer := informerFactory.Core().V1().Secrets()
	_, err := informer.Informer().AddEventHandler(r)
	return err
}

func (r *Registry) OnAdd(obj interface{}, _ bool) {
	secret, ok := secretFrom(obj)
	if !ok {
		return
	}

	agentType, err := parseAgentType(secret)
	if err != nil {
		klog.Errorf("Error when adding agent type: %v", err)
		return
	}

	klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	r.set(agentType)
}

func (r *Registry) OnUpdate(oldObj, newObj interface{}) {
	newSecret, ok := secretFrom(newObj)
	if !ok {
		return
	}

	oldSecret, ok := secretFrom(oldObj)
	if !ok {
		return
	}

	if newSecret.ResourceVersion == oldSecret.ResourceVersion {
		return
	}

	newAgentType, err := parseAgentType(newSecret)
	if err != nil {
		klog.Errorf("Error when parsing agent type, ignoring the update: %v", err)
		return
	}

	// Only the old name is needed. The old secret may have been invalid,
	// and that must not stop the fixed one from being registered.
	oldAgentTypeName, _ := findAgentTypeName(oldSecret)

	// If the agent type name remained the same, we only need to update it.
	if oldAgentTypeName == newAgentType.AgentTypeName {
		klog.Infof("Agent type updated: %s", newAgentType.AgentTypeName)
		r.set(newAgentType)
		return
	}

	// Otherwise, we need to delete the old one before adding the new one
	if oldAgentTypeName != "" {
		klog.Infof("Agent type deleted: %s", oldAgentTypeName)
		r.remove(oldAgentTypeName)
	}

	klog.Infof("Agent type added: %s", newAgentType.AgentTypeName)
	r.set(newAgentType)
}

func (r *Registry) OnDelete(obj interface{}) {
	secret, ok := secretFrom(obj)
	if !ok {
		return
	}

	agentTypeName, err := findAgentTypeName(secret)
	if err != nil {
		klog.Errorf("Error when deleting agent type: %v", err)
		return
	}

	klog.Infof("Agent type deleted: %s", agentTypeName)
	r.remove(agentTypeName)
}

// For deletes, the informer can hand us a tombstone instead of the secret:
// when its watch drops and the relist finds the secret already gone, it
// wraps the last known object in a cache.DeletedFinalStateUnknown.
func secretFrom(obj interface{}) (*v1.Secret, bool) {
	if secret, ok := obj.(*v1.Secret); ok {
		return secret, true
	}

	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if !ok {
		klog.Warningf("Expected a secret, got %T", obj)
		return nil, false
	}

	secret, ok := tombstone.Obj.(*v1.Secret)
	if !ok {
		klog.Warningf("Expected a secret in tombstone, got %T", tombstone.Obj)
		return nil, false
	}

	return secret, true
}

func (r *Registry) All() []*AgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := []*AgentType{}
	for _, v := range r.agentTypes {
		types = append(types, v)
	}

	return types
}

func (r *Registry) Get(name string) *AgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	v, ok := r.agentTypes[name]
	if !ok {
		return nil
	}

	return v
}

func (r *Registry) set(agentType *AgentType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.agentTypes[agentType.AgentTypeName] = agentType
}

func (r *Registry) remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.agentTypes, name)
}

func findAgentTypeName(secret *v1.Secret) (string, error) {
	agentTypeName, ok := secret.Data["agentTypeName"]
	if !ok {
		return "", fmt.Errorf("no 'agentTypeName' field in secret '%s'", secret.GetName())
	}

	return string(agentTypeName), nil
}

func parseAgentType(secret *v1.Secret) (*AgentType, error) {
	agentTypeName, ok := secret.Data["agentTypeName"]
	if !ok {
		return nil, fmt.Errorf("no agentTypeName field in secret '%s'", secret.GetName())
	}

	registrationToken, ok := secret.Data["registrationToken"]
	if !ok {
		return nil, fmt.Errorf("no registrationToken field in secret '%s'", secret.GetName())
	}

	agentStartupParameters := []string{}
	if parameters, ok := secret.Data["agentStartupParameters"]; ok && string(parameters) != "" {
		for _, v := range strings.Split(string(parameters), " ") {
			parameter := strings.Trim(strings.Trim(v, "\n"), " ")
			agentStartupParameters = append(agentStartupParameters, parameter)
		}
	}

	agentPodSpec, err := podspec.Parse(string(secret.Data["agentPodSpec"]))
	if err != nil {
		return nil, fmt.Errorf("invalid agentPodSpec field in secret '%s': %v", secret.GetName(), err)
	}

	return &AgentType{
		SecretName:             secret.GetName(),
		AgentTypeName:          string(agentTypeName),
		RegistrationToken:      string(registrationToken),
		AgentStartupParameters: agentStartupParameters,
		AgentPodSpec:           agentPodSpec,
	}, nil
}
