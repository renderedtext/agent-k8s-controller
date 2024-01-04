package semaphore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	Endpoint string
	Token    string
}

func NewClient(endpoint, token string) *Client {
	return &Client{
		Endpoint: endpoint,
		Token:    token,
	}
}

type APIResponse struct {
	Jobs []APIJob `json:"jobs" yaml:"jobs"`
}

type APIJob struct {
	Metadata APIJobMetadata `json:"metadata" yaml:"metadata"`
	Spec     APIJobSpec     `json:"spec" yaml:"spec"`
}

// There are more fields here, but I only care about the ID for now.
type APIJobMetadata struct {
	ID string `json:"id" yaml:"id"`
}

// There are more fields here, but I only care about the machine type for now.
type APIJobSpec struct {
	Agent struct {
		Machine struct {
			Type string
		}
	}
}

type JobRequest struct {
	JobID       string
	MachineType string
}

func (a *Client) JobsFor(machineTypes []string) ([]JobRequest, error) {
	URL := fmt.Sprintf("https://%s/api/v1alpha/jobs?states=PENDING&states=QUEUED", a.Endpoint)
	req, err := http.NewRequest(http.MethodGet, URL, nil)
	if err != nil {
		return []JobRequest{}, err
	}

	req.Header.Add("Authorization", fmt.Sprintf("Token %s", a.Token))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return []JobRequest{}, err
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return []JobRequest{}, err
	}

	apiResponse := APIResponse{}
	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return []JobRequest{}, err
	}

	js := []JobRequest{}
	for _, j := range apiResponse.Jobs {
		if in(machineTypes, j.Spec.Agent.Machine.Type) {
			js = append(js, JobRequest{JobID: j.Metadata.ID, MachineType: j.Spec.Agent.Machine.Type})
		}
	}

	return js, nil
}

func in(list []string, item string) bool {
	for _, i := range list {
		if i == item {
			return true
		}
	}

	return false
}
