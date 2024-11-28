package agenttypes

import (
	"fmt"
	"strings"

	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
)

type AgentType struct {
	SecretName             string
	AgentTypeName          string
	RegistrationToken      string
	AgentStartupParameters []string
}

type Registry struct {
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
	secret := obj.(*v1.Secret)
	agentType, err := parseAgentType(secret)
	if err != nil {
		klog.Errorf("Error when adding agent type: %v", err)
		return
	}

	klog.Infof("Agent type added: %s", agentType.AgentTypeName)
	r.agentTypes[agentType.AgentTypeName] = agentType
}

func (r *Registry) OnUpdate(oldObj, newObj interface{}) {
	newSecret := newObj.(*v1.Secret)
	oldSecret := oldObj.(*v1.Secret)
	if newSecret.ResourceVersion == oldSecret.ResourceVersion {
		return
	}

	newAgentType, err := parseAgentType(newSecret)
	if err != nil {
		klog.Errorf("Error when parsing agent type: %v", err)
		return
	}

	oldAgentType, err := parseAgentType(oldSecret)
	if err != nil {
		klog.Errorf("Error when parsing agent type: %v", err)
		return
	}

	// If the agent type name remained the same, we only need to update it.
	if oldAgentType.AgentTypeName == newAgentType.AgentTypeName {
		klog.Infof("Agent type updated: %s", newAgentType.AgentTypeName)
		r.agentTypes[newAgentType.AgentTypeName] = newAgentType
		return
	}

	// Otherwise, we need to delete the old one before adding the new one
	klog.Infof("Agent type deleted: %s", oldAgentType.AgentTypeName)
	delete(r.agentTypes, oldAgentType.AgentTypeName)
	klog.Infof("Agent type added: %s", newAgentType.AgentTypeName)
	r.agentTypes[newAgentType.AgentTypeName] = newAgentType
}

func (r *Registry) OnDelete(obj interface{}) {
	secret := obj.(*v1.Secret)
	agentTypeName, err := findAgentTypeName(secret)
	if err != nil {
		fmt.Printf("Error when deleting agent type: %v\n", err)
		return
	}

	klog.Infof("Agent type deleted: %s", agentTypeName)
	delete(r.agentTypes, agentTypeName)
}

func (r *Registry) All() []*AgentType {
	types := []*AgentType{}
	for _, v := range r.agentTypes {
		types = append(types, v)
	}

	return types
}

func (r *Registry) Get(name string) *AgentType {
	v, ok := r.agentTypes[name]
	if !ok {
		return nil
	}

	return v
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

	return &AgentType{
		SecretName:             secret.GetName(),
		AgentTypeName:          string(agentTypeName),
		RegistrationToken:      string(registrationToken),
		AgentStartupParameters: agentStartupParameters,
	}, nil
}
