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
export SEMAPHORE_AGENT_API_TOKEN=???
export SEMAPHORE_AGENT_MACHINE_TYPE=s1-just-testing
export SEMAPHORE_ENDPOINT=rtx.sxpreprod.com
export KUBERNETES_NAMESPACE=default
export SEMAPHORE_AGENT_IMAGE=semaphoreci/agent:v2.2.13
export SEMAPHORE_AGENT_CONFIG_FROM_SECRET=semaphore-agent-type-config
export KUBERNETES_SERVICE_ACCOUNT=semaphore-agent-svc-account
export MAX_PARALLEL_JOBS=10

# Build and start controller
go build -o controller main.go
./controller &>/tmp/controller.logs
```

### Next steps

- [ ] Start agent for specific job
- [ ] Allow a little bit of history
- [ ] Multiple agent types?
- [ ] How would we configure hooks?
- [ ] How would we configure other pod spec for job?
