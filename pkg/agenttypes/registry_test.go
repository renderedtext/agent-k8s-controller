package agenttypes

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
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

	t.Run("on add -> no pod spec", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		r.OnAdd(agentTypeSecret(agentTypeName, "1", ""), false)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Nil(t, agentType.AgentPodSpec)
	})

	t.Run("on add -> with pod spec", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		r.OnAdd(agentTypeSecret(agentTypeName, "1", "nodeSelector:\n  pool: agents\n"), false)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.NotNil(t, agentType.AgentPodSpec)
		require.Equal(t, map[string]string{"pool": "agents"}, agentType.AgentPodSpec.NodeSelector)
	})

	t.Run("on add -> invalid pod spec -> agent type is not added", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		r.OnAdd(agentTypeSecret(agentTypeName, "1", "nodeSelectors:\n  pool: agents\n"), false)
		require.Nil(t, r.Get(agentTypeName))
	})

	t.Run("on update -> invalid pod spec -> previous configuration is kept", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		old := agentTypeSecret(agentTypeName, "1", "nodeSelector:\n  pool: agents\n")
		r.OnAdd(old, false)

		new := agentTypeSecret(agentTypeName, "2", "nodeSelectors:\n  pool: other\n")
		r.OnUpdate(old, new)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, map[string]string{"pool": "agents"}, agentType.AgentPodSpec.NodeSelector)
	})

	t.Run("on update -> fix after an invalid update is applied", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		valid := agentTypeSecret(agentTypeName, "1", "nodeSelector:\n  pool: agents\n")
		r.OnAdd(valid, false)

		invalid := agentTypeSecret(agentTypeName, "2", "nodeSelectors:\n  pool: other\n")
		r.OnUpdate(valid, invalid)

		fixed := agentTypeSecret(agentTypeName, "3", "nodeSelector:\n  pool: other\n")
		r.OnUpdate(invalid, fixed)

		agentType := r.Get(agentTypeName)
		require.NotNil(t, agentType)
		require.Equal(t, map[string]string{"pool": "other"}, agentType.AgentPodSpec.NodeSelector)
	})

	t.Run("on update -> secret that was never valid is added once fixed", func(t *testing.T) {
		agentTypeName := randAgentTypeName()
		invalid := agentTypeSecret(agentTypeName, "1", "nodeSelectors:\n  pool: agents\n")
		r.OnAdd(invalid, false)
		require.Nil(t, r.Get(agentTypeName))

		fixed := agentTypeSecret(agentTypeName, "2", "nodeSelector:\n  pool: agents\n")
		r.OnUpdate(invalid, fixed)
		require.NotNil(t, r.Get(agentTypeName))

		// a secret that had no name at all
		otherName := randAgentTypeName()
		unnamed := agentTypeSecret(otherName, "1", "")
		delete(unnamed.Data, "agentTypeName")
		r.OnAdd(unnamed, false)

		named := agentTypeSecret(otherName, "2", "")
		r.OnUpdate(unnamed, named)
		require.NotNil(t, r.Get(otherName))
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

	t.Run("on update -> old is deleted, new is added", func(t *testing.T) {
		oldAgentTypeName := randAgentTypeName()
		secretName := randSecretName()
		oldToken := randomString(t)
		require.Nil(t, r.Get(oldAgentTypeName))

		old := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, ResourceVersion: "1"},
			Data: map[string][]byte{
				"agentTypeName":     []byte(oldAgentTypeName),
				"registrationToken": []byte(oldToken),
			},
		}

		// agent type is added
		r.OnAdd(old, false)
		require.NotNil(t, r.Get(oldAgentTypeName))

		// agent type is updated with different name
		new := old.DeepCopy()
		newAgentTypeName := randAgentTypeName()
		new.Data["agentTypeName"] = []byte(newAgentTypeName)
		new.ObjectMeta.ResourceVersion = "2"
		r.OnUpdate(old, new)

		// old is deleted, new is added
		require.Nil(t, r.Get(oldAgentTypeName))
		require.NotNil(t, r.Get(newAgentTypeName))
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

// A secret's change or deletion must only touch the agent type that secret defined.
func Test__RegistryIsKeyedBySecret(t *testing.T) {
	t.Run("rename through an ignored invalid update leaves no stale agent type", func(t *testing.T) {
		r, _ := NewRegistry()
		valid := namedSecret("s1", "s1-x", "1")
		r.OnAdd(valid, false)

		invalidRename := namedSecret("s1", "s1-y", "2")
		invalidRename.Data["agentPodSpec"] = []byte("nodeSelectors: {}")
		r.OnUpdate(valid, invalidRename)
		require.Equal(t, []string{"s1-x"}, names(r))

		fixedRename := namedSecret("s1", "s1-y", "3")
		r.OnUpdate(invalidRename, fixedRename)
		require.Equal(t, []string{"s1-y"}, names(r))

		r.OnDelete(fixedRename)
		require.Empty(t, names(r))
	})

	t.Run("same name in two secrets -> the oldest is used, the other takes over once it is deleted", func(t *testing.T) {
		r, _ := NewRegistry()
		old := createdAt(namedSecret("s2", "s1-x", "1"), 0)
		r.OnAdd(old, false)

		// a new secret replaces the old one, which is then deleted
		replacement := createdAt(namedSecret("s1", "s1-x", "1"), time.Minute)
		replacement.Data["registrationToken"] = []byte("new-token")
		r.OnAdd(replacement, false)
		require.Equal(t, []string{"s1-x"}, names(r))
		require.Equal(t, "s2", r.Get("s1-x").SecretName)

		r.OnDelete(old)
		require.Equal(t, []string{"s1-x"}, names(r))
		require.Equal(t, "s1", r.Get("s1-x").SecretName)
		require.Equal(t, "new-token", r.Get("s1-x").RegistrationToken)
	})

	t.Run("same name in two secrets -> the winner does not depend on load order", func(t *testing.T) {
		for _, order := range [][]string{{"s1", "s2"}, {"s2", "s1"}} {
			r, _ := NewRegistry()
			secrets := map[string]*v1.Secret{
				"s1": createdAt(namedSecret("s1", "s1-x", "1"), time.Minute),
				"s2": createdAt(namedSecret("s2", "s1-x", "1"), 0),
			}

			for _, name := range order {
				r.OnAdd(secrets[name], false)
			}

			require.Equal(t, "s2", r.Get("s1-x").SecretName, order)
		}
	})

	t.Run("rename onto a name another secret holds -> the old name goes, the older secret keeps the new one", func(t *testing.T) {
		r, _ := NewRegistry()
		holder := createdAt(namedSecret("s1", "s1-x", "1"), 0)
		r.OnAdd(holder, false)

		other := createdAt(namedSecret("s2", "s1-y", "1"), time.Minute)
		r.OnAdd(other, false)

		renamed := createdAt(namedSecret("s2", "s1-x", "2"), time.Minute)
		r.OnUpdate(other, renamed)
		require.Equal(t, []string{"s1-x"}, names(r))
		require.Equal(t, "s1", r.Get("s1-x").SecretName)

		r.OnDelete(holder)
		require.Equal(t, "s2", r.Get("s1-x").SecretName)
	})

	t.Run("fixing a rejected copy under another name keeps the original", func(t *testing.T) {
		r, _ := NewRegistry()
		original := namedSecret("s1", "s1-x", "1")
		r.OnAdd(original, false)

		invalid := namedSecret("s2", "s1-x", "1")
		delete(invalid.Data, "registrationToken")
		r.OnAdd(invalid, false)

		fixed := namedSecret("s2", "s1-y", "2")
		r.OnUpdate(invalid, fixed)
		require.Equal(t, []string{"s1-x", "s1-y"}, names(r))
	})
}

func namedSecret(secretName, agentTypeName, resourceVersion string) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "semaphore", ResourceVersion: resourceVersion},
		Data: map[string][]byte{
			"agentTypeName":     []byte(agentTypeName),
			"registrationToken": []byte("token"),
		},
	}
}

func createdAt(secret *v1.Secret, offset time.Duration) *v1.Secret {
	secret.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(offset))
	return secret
}

func names(r *Registry) []string {
	names := []string{}
	for _, agentType := range r.All() {
		names = append(names, agentType.AgentTypeName)
	}

	slices.Sort(names)
	return names
}

// The informer reports a tombstone instead of the secret when its watch drops
// and the relist finds the secret already gone.
func Test__RegistryOnDeleteHandlesTombstone(t *testing.T) {
	r, err := NewRegistry()
	require.NoError(t, err)

	agentTypeName := randAgentTypeName()
	secret := agentTypeSecret(agentTypeName, "1", "")
	r.OnAdd(secret, false)
	require.NotNil(t, r.Get(agentTypeName))

	tombstone := cache.DeletedFinalStateUnknown{
		Key: fmt.Sprintf("default/%s", secret.Name),
		Obj: secret,
	}

	require.NotPanics(t, func() { r.OnDelete(tombstone) })
	require.Nil(t, r.Get(agentTypeName))

	// an unexpected type is ignored rather than fatal
	require.NotPanics(t, func() { r.OnDelete(cache.DeletedFinalStateUnknown{Key: "x", Obj: "not a secret"}) })
	require.NotPanics(t, func() { r.OnDelete("not a secret") })
	require.NotPanics(t, func() { r.OnAdd("not a secret", false) })
	require.NotPanics(t, func() { r.OnUpdate("not a secret", secret) })
	require.NotPanics(t, func() { r.OnUpdate(secret, "not a secret") })
}

// The informer updates agent types while the controller's polling loop reads them.
func Test__RegistryIsSafeForConcurrentUse(t *testing.T) {
	r, err := NewRegistry()
	require.NoError(t, err)

	agentTypeName := randAgentTypeName()
	r.OnAdd(agentTypeSecret(agentTypeName, "0", ""), false)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 1; i <= 200; i++ {
			old := agentTypeSecret(agentTypeName, fmt.Sprint(i-1), "")
			new := agentTypeSecret(agentTypeName, fmt.Sprint(i), "")
			r.OnUpdate(old, new)

			other := agentTypeSecret(randAgentTypeName(), "1", "")
			r.OnAdd(other, false)
			r.OnDelete(other)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = r.All()
			_ = r.Get(agentTypeName)
		}
	}()

	wg.Wait()
	require.NotNil(t, r.Get(agentTypeName))
}

func agentTypeSecret(agentTypeName, resourceVersion, podSpec string) *v1.Secret {
	s := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "secret-" + agentTypeName, ResourceVersion: resourceVersion},
		Data: map[string][]byte{
			"agentTypeName":     []byte(agentTypeName),
			"registrationToken": []byte("token"),
		},
	}

	if podSpec != "" {
		s.Data["agentPodSpec"] = []byte(podSpec)
	}

	return s
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
