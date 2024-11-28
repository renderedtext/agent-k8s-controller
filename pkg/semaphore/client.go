package semaphore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/renderedtext/agent-k8s-stack/pkg/agenttypes"
)

type Client struct {
	Endpoint string
}

func NewClient(endpoint string) *Client {
	return &Client{
		Endpoint: endpoint,
	}
}

type Response struct {
	Jobs []Job `json:"jobs" yaml:"jobs"`
}

// There are more fields here, but I only care about the ID for now.
type Job struct {
	ID string `json:"id" yaml:"id"`
}

type JobRequest struct {
	JobID       string
	MachineType string
}

// Respect the protocol if it's specified
// Otherwise, default to https
func (a *Client) getURL() string {
	if strings.HasPrefix(a.Endpoint, "http://") || strings.HasPrefix(a.Endpoint, "https://") {
		return a.Endpoint + "/api/v1/self_hosted_agents/jobs"
	}

	return "https://" + a.Endpoint + "/api/v1/self_hosted_agents/jobs"
}

func (a *Client) JobsFor(agentTypes []*agenttypes.AgentType) ([]JobRequest, error) {
	jobRequests := []JobRequest{}

	for _, agentType := range agentTypes {
		jobs, err := a.getJobsFor(agentType)
		if err != nil {
			return []JobRequest{}, err
		}

		for _, j := range jobs {
			jobRequest := JobRequest{
				JobID:       j,
				MachineType: agentType.AgentTypeName,
			}
			jobRequests = append(jobRequests, jobRequest)
		}
	}

	return jobRequests, nil
}

func (a *Client) getJobsFor(agentType *agenttypes.AgentType) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, a.getURL(), nil)
	if err != nil {
		return []string{}, err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Token %s", agentType.RegistrationToken))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return []string{}, err
	}

	apiResponse := Response{}
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return []string{}, err
	}

	jobs := []string{}
	for _, j := range apiResponse.Jobs {
		jobs = append(jobs, j.ID)
	}

	return jobs, nil
}

func in(list []string, item string) bool {
	for _, i := range list {
		if i == item {
			return true
		}
	}

	return false
}
