package agenttypes

import (
	"fmt"
	"strings"
	"sync"
	"time"

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
	mu      sync.RWMutex
	secrets map[string]registered
}

type registered struct {
	agentType *AgentType
	created   time.Time
}

func NewRegistry() (*Registry, error) {
	return &Registry{
		secrets: map[string]registered{},
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

	r.set(secretKey(secret), secret.CreationTimestamp.Time, agentType)
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

	r.set(secretKey(newSecret), newSecret.CreationTimestamp.Time, agentType)
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
	for _, v := range r.active() {
		types = append(types, v)
	}

	return types
}

func (r *Registry) Get(name string) *AgentType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.active()[name]
}

// Every valid secret is kept. When several define the same agent type, the
// oldest one is used, so deleting it hands the agent type to the next one.
// Callers must hold mu.
func (r *Registry) active() map[string]*AgentType {
	winners := map[string]string{}
	for key, s := range r.secrets {
		current, ok := winners[s.agentType.AgentTypeName]
		if !ok || r.older(key, current) {
			winners[s.agentType.AgentTypeName] = key
		}
	}

	active := map[string]*AgentType{}
	for name, key := range winners {
		active[name] = r.secrets[key].agentType
	}

	return active
}

func (r *Registry) older(a, b string) bool {
	if !r.secrets[a].created.Equal(r.secrets[b].created) {
		return r.secrets[a].created.Before(r.secrets[b].created)
	}

	return a < b
}

func (r *Registry) set(key string, created time.Time, agentType *AgentType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous, ok := r.secrets[key]
	r.secrets[key] = registered{agentType: agentType, created: created}

	switch {
	case !ok:
		klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	case previous.agentType.AgentTypeName == agentType.AgentTypeName:
		klog.Infof("Agent type updated: %s", agentType.AgentTypeName)
	default:
		klog.Infof("Agent type deleted: %s", previous.agentType.AgentTypeName)
		klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	}

	if winner := r.active()[agentType.AgentTypeName]; winner != agentType {
		klog.Warningf("Agent type %s is also defined by the older secret '%s', which is used instead of secret '%s'",
			agentType.AgentTypeName, winner.SecretName, agentType.SecretName)
	}
}

func (r *Registry) remove(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.secrets[key]
	if !ok {
		return
	}

	delete(r.secrets, key)
	if next, ok := r.active()[s.agentType.AgentTypeName]; ok {
		klog.Infof("Agent type %s now uses secret '%s'", s.agentType.AgentTypeName, next.SecretName)
		return
	}

	klog.Infof("Agent type deleted: %s", s.agentType.AgentTypeName)
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
