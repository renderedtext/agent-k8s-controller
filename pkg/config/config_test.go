package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test__AgentExecutionStrategy(t *testing.T) {
	t.Run("defaults to exec when unset", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_KUBERNETES_EXECUTION_STRATEGY", "")
		require.Equal(t, ExecutionStrategyExec, agentExecutionStrategy())
	})

	t.Run("uses attach when set to attach", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_KUBERNETES_EXECUTION_STRATEGY", "attach")
		require.Equal(t, ExecutionStrategyAttach, agentExecutionStrategy())
	})

	t.Run("falls back to exec on an invalid value", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_KUBERNETES_EXECUTION_STRATEGY", "bogus")
		require.Equal(t, ExecutionStrategyExec, agentExecutionStrategy())
	})
}
