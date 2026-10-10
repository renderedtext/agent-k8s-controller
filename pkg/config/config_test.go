package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func Test__AgentPodSpec(t *testing.T) {
	t.Run("not set -> no spec", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_POD_SPEC", "")
		cfg, err := NewConfigFromEnv("example.semaphoreci.com")
		require.NoError(t, err)
		require.Nil(t, cfg.AgentPodSpec)
	})

	t.Run("valid -> spec is loaded", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_POD_SPEC", `{"nodeSelector":{"pool":"agents"}}`)
		cfg, err := NewConfigFromEnv("example.semaphoreci.com")
		require.NoError(t, err)
		require.NotNil(t, cfg.AgentPodSpec)
		require.Equal(t, map[string]string{"pool": "agents"}, cfg.AgentPodSpec.NodeSelector)
	})

	t.Run("invalid -> error", func(t *testing.T) {
		t.Setenv("SEMAPHORE_AGENT_POD_SPEC", "nodeSelectors:\n  pool: agents\n")
		_, err := NewConfigFromEnv("example.semaphoreci.com")
		require.ErrorContains(t, err, "invalid SEMAPHORE_AGENT_POD_SPEC")
		require.ErrorContains(t, err, `unknown field "nodeSelectors"`)
	})
}
