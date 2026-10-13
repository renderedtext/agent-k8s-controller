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

// Agent types are keyed by the secret that defines them, so a change or a
// deletion only touches what that secret defined. The informer writes them
// while the controller's polling loop reads them, so the map is guarded by mu.
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

	r.set(secretKey(secret), agentType)
}

// An invalid update is ignored as a whole, so the secret
// keeps the configuration it had before the update.
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

	agentType, err := parseAgentType(newSecret)
	if err != nil {
		klog.Errorf("Error when parsing agent type, ignoring the update: %v", err)
		return
	}

	r.set(secretKey(newSecret), agentType)
}

func (r *Registry) OnDelete(obj interface{}) {
	secret, ok := secretFrom(obj)
	if !ok {
		return
	}

	r.remove(secretKey(secret))
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

func secretKey(secret *v1.Secret) string {
	return secret.Namespace + "/" + secret.Name
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

	for _, v := range r.agentTypes {
		if v.AgentTypeName == name {
			return v
		}
	}

	return nil
}

// The first secret to define an agent type keeps it. Another secret
// using the same name is ignored, since jobs could only go to one of them.
func (r *Registry) set(key string, agentType *AgentType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for otherKey, other := range r.agentTypes {
		if otherKey != key && other.AgentTypeName == agentType.AgentTypeName {
			klog.Errorf("Agent type %s is already defined by secret '%s' - ignoring secret '%s'",
				agentType.AgentTypeName, other.SecretName, agentType.SecretName)
			return
		}
	}

	previous, ok := r.agentTypes[key]
	r.agentTypes[key] = agentType

	switch {
	case !ok:
		klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	case previous.AgentTypeName == agentType.AgentTypeName:
		klog.Infof("Agent type updated: %s", agentType.AgentTypeName)
	default:
		klog.Infof("Agent type deleted: %s", previous.AgentTypeName)
		klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	}
}

func (r *Registry) remove(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if agentType, ok := r.agentTypes[key]; ok {
		klog.Infof("Agent type deleted: %s", agentType.AgentTypeName)
		delete(r.agentTypes, key)
	}
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
