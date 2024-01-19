package agenttypes

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test__Registry(t *testing.T) {
	r, err := NewRegistry()
	require.NoError(t, err)

	t.Run("on add -> no startup parameters", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		secretName := randSecretName()
		token := randomString(t)
		require.Nil(t, r.Get(agentTypeName))

		s := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName},
			Data: map[string][]byte{
				"agentTypeName":     []byte(agentTypeName),
				"registrationToken": []byte(token),
			},
		}

		r.OnAdd(s, false)
		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, agentTypeName, agentType.AgentTypeName)
		require.Equal(t, token, agentType.RegistrationToken)
		require.Equal(t, secretName, agentType.SecretName)
		require.Empty(t, agentType.AgentStartupParameters)
	})

	t.Run("on add -> with startup parameters", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		secretName := randSecretName()
		token := randomString(t)
		require.Nil(t, r.Get(agentTypeName))

		s := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName},
			Data: map[string][]byte{
				"agentTypeName":          []byte(agentTypeName),
				"registrationToken":      []byte(token),
				"agentStartupParameters": []byte("--kubernetes-pod-spec my-spec"),
			},
		}

		r.OnAdd(s, false)
		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, agentTypeName, agentType.AgentTypeName)
		require.Equal(t, token, agentType.RegistrationToken)
		require.Equal(t, []string{"--kubernetes-pod-spec", "my-spec"}, agentType.AgentStartupParameters)
	})

	t.Run("on update, same version -> nothing is updated", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		secretName := randSecretName()
		oldToken := randomString(t)
		require.Nil(t, r.Get(agentTypeName))

		old := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, ResourceVersion: "1"},
			Data: map[string][]byte{
				"agentTypeName":     []byte(agentTypeName),
				"registrationToken": []byte(oldToken),
			},
		}

		// agent type is added
		r.OnAdd(old, false)

		// update with same resource version
		// does not update the agent type information.
		new := old.DeepCopy()
		new.Data["registrationToken"] = []byte(randomString(t))
		r.OnUpdate(old, new)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, oldToken, agentType.RegistrationToken)
	})

	t.Run("on update -> agent type is updated", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		secretName := randSecretName()
		oldToken := randomString(t)
		require.Nil(t, r.Get(agentTypeName))

		old := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, ResourceVersion: "1"},
			Data: map[string][]byte{
				"agentTypeName":     []byte(agentTypeName),
				"registrationToken": []byte(oldToken),
			},
		}

		// agent type is added
		r.OnAdd(old, false)

		// update with same resource version
		// does not update the agent type information.
		new := old.DeepCopy()
		newToken := randomString(t)
		new.Data["registrationToken"] = []byte(newToken)
		new.ObjectMeta.ResourceVersion = "2"
		r.OnUpdate(old, new)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, newToken, agentType.RegistrationToken)
	})

	t.Run("on delete -> agent type is deleted", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		secretName := randSecretName()
		token := randomString(t)
		require.Nil(t, r.Get(agentTypeName))

		s := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, ResourceVersion: "1"},
			Data: map[string][]byte{
				"agentTypeName":     []byte(agentTypeName),
				"registrationToken": []byte(token),
			},
		}

		// agent type is added
		r.OnAdd(s, false)
		require.NotNil(t, r.Get(agentTypeName))

		// agent type is deleted
		r.OnDelete(s)
		require.Nil(t, r.Get(agentTypeName))
	})
}

func randAgentTypeName() string {
	return fmt.Sprintf("s1-test-%d", rand.Int())
}

func randSecretName() string {
	return fmt.Sprintf("secret-%d", rand.Int())
}

func randomString(t *testing.T) string {
	buffer := make([]byte, 15)

	// #nosec
	_, err := rand.Read(buffer)
	require.NoError(t, err)
	return base64.URLEncoding.EncodeToString(buffer)
}
