# End-to-end test

`run.sh` builds the controller from this checkout, runs it in a throwaway [k3s](https://k3s.io) cluster in Docker, and has it serve a real Semaphore self-hosted agent type. It then checks how the controller handles the jobs queued for that type:

- an invalid `SEMAPHORE_AGENT_POD_SPEC` stops the controller at startup, and an invalid change to the agent type secret is ignored
- an agent type's `agentPodSpec` replaces the controller's, and an agent pod no node can take is reported with its reason after `JOB_START_TIMEOUT` and replaced
- once the secret is fixed, agent pods run on a tainted node with the controller's node selector, toleration, topology spread constraint and priority class
- job pods get the agent's `--kubernetes-pod-spec` and are owned by their agent pod
- no more than `MAX_PARALLEL_JOBS` agent jobs run at once
- jobs keep running and finish through a controller restart
- no job pods or secrets are left behind, and the controller never restarts on its own

It does not queue the jobs itself. In CI, the `E2E tests` promotion runs `.semaphore/e2e.yml`, whose second block runs its jobs on the agent type under test.

## In CI

1. Create a self-hosted agent type in the organization that runs this project's CI. Its jobs come from the same pipeline, so it has to be the same organization.
2. Create the `E2E` deployment target with these environment variables:
   - `E2E_SEMAPHORE_ENDPOINT`: the organization's endpoint, e.g. `example.semaphoreci.com`
   - `E2E_AGENT_TOKEN`: the agent type's registration token
   - `E2E_AGENT_TYPE` (optional): the agent type's name, checked against the promotion's `AGENT_TYPE` parameter
3. Start the `E2E tests` promotion, with `AGENT_TYPE` set to the agent type's name.

Only one end-to-end run can use the agent type at a time, so the pipeline has its own queue. No other controller may serve the agent type while it runs.

## Locally

Queue `E2E_EXPECTED_JOBS` jobs for the agent type yourself, while or before the test runs, then:

```bash
E2E_SEMAPHORE_ENDPOINT=example.semaphoreci.com \
E2E_AGENT_TYPE=s1-my-agent-type \
E2E_EXPECTED_JOBS=4 \
E2E_AGENT_TOKEN="$(cat token-file)" \
test/e2e/run.sh
```

The agent image defaults to `semaphoreci/agent:v2.6.0`, which is built for amd64. On another architecture, build the agent image locally and set `E2E_AGENT_IMAGE`. `E2E_PREFIX` names the Docker objects, and `E2E_KEEP=1` keeps the cluster for inspection.
