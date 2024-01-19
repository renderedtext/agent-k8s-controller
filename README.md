# Semaphore agent controller for Kubernetes

A Kubernetes controller that runs Semaphore jobs in Kubernetes.

## Installation

### Requirements

- A Kubernetes cluster
- A Semaphore API token

### Configuration

All the configuration for the controller is provided through environment variables:
- **SEMAPHORE_API_TOKEN**: the Semaphore API token used by the controller to inspect the job queues.
- **SEMAPHORE_ENDPOINT**: the Semaphore endpoint, e.g. `<your-organization>.semaphoreci.com`.
- **KUBERNETES_NAMESPACE**: in which Kubernetes namespace the controller should create the resources for the Semaphore jobs. If nothing is specified, the default namespace is used.
- **SEMAPHORE_AGENT_IMAGE**: the [Semaphore agent](https://github.com/semaphoreci/agent) image to use when creating agents. If nothing is specified, `semaphoreci/agent:latest` is used.
- **MAX_PARALLEL_JOBS**: how much Semaphore jobs to run in parallel. By default, 10.
- **KUBERNETES_SERVICE_ACCOUNT**: the Kubernetes service account name to use on the pods created to run the [Semaphore agent](https://github.com/semaphoreci/agent).
- **SEMAPHORE_AGENT_LABELS**: a comma-separated list of Kubernetes labels to apply on all resources created by the controller.
- **SEMAPHORE_AGENT_STARTUP_PARAMETERS**: any additional [Semaphore agent configuration parameters](https://docs.semaphoreci.com/ci-cd-environment/configure-self-hosted-agent/) to pass to the agents being created.
