### Testing locally

```bash
# Create k8s cluster
sem-version go 1.21
curl -sLO https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64 && install minikube-linux-amd64 /tmp/
/tmp/minikube-linux-amd64 config set WantUpdateNotification false
/tmp/minikube-linux-amd64 start --driver=docker
eval $(/tmp/minikube-linux-amd64 docker-env)

# Create required k8s resources
kubectl apply -f resources.yml

# Expose configuration parameters
export SEMAPHORE_API_TOKEN=???
export SEMAPHORE_ENDPOINT=rtx.sxpreprod.com
export KUBERNETES_NAMESPACE=default
export SEMAPHORE_AGENT_IMAGE=semaphoreci/agent:v2.2.14
export KUBERNETES_SERVICE_ACCOUNT=semaphore-agent-svc-account
export MAX_PARALLEL_JOBS=10
export SEMAPHORE_AGENT_STARTUP_PARAMETERS='--kubernetes-executor-pod-spec WHATEVER --pre-job-hook-path /opt/semaphore/agent/hooks/pre-job.sh --source-pre-job-hook'

# Build and start controller
go build -o controller main.go
./controller &>/tmp/controller.logs
```
