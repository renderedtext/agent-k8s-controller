package config

const (
	AgentTypeLabel           = "semaphoreci.com/agent-type"
	JobIDLabel               = "semaphoreci.com/job-id"
	ResourceTypeLabel        = "semaphoreci.com/resource-type"
	AgentTypeResourceType    = "agent-type-configuration"
	SemaphoreJobResourceType = "semaphore-job"
)

type Config struct {
	Namespace              string
	ServiceAccountName     string
	AgentImage             string
	AgentStartupParameters []string
	Labels                 []string
	MaxParallelJobs        int
	SemaphoreEndpoint      string
}
