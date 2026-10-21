package snmp

import (
	"context"
	"errors"
	"testing"

	"github.com/gosnmp/gosnmp"
	enclave "github.com/jeremyhahn/go-enclave"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// refusingSealer is a sealer whose provider has failed.
type refusingSealer struct{ err error }

func (r refusingSealer) Seal(context.Context, []byte) (enclave.Sealed, error) { return nil, r.err }

// afterFirstSealer seals the first secret and refuses the rest, so a seal
// fails with something already sealed.
type afterFirstSealer struct {
	sealed []enclave.Sealed
}

func (s *afterFirstSealer) Seal(ctx context.Context, b []byte) (enclave.Sealed, error) {
	if len(s.sealed) > 0 {
		enclave.Wipe(b)
		return nil, errors.New("provider down")
	}
	sealed, err := credential.Default().Seal(ctx, b)
	if err == nil {
		s.sealed = append(s.sealed, sealed)
	}
	return sealed, err
}

func TestSealCredentialsTakesTheSecretsOffTheDevice(t *testing.T) {
	device := &config.Device{
		Name: "r1", SNMPVersion: "3", Community: "c0mm",
		Username: "u", AuthPassword: "authpass1", PrivPassword: "privpass1",
	}
	creds, err := SealCredentials(context.Background(), credential.Default(), device)
	require.NoError(t, err)
	t.Cleanup(func() { _ = creds.Destroy() })

	assert.Empty(t, device.Community)
	assert.Empty(t, device.AuthPassword)
	assert.Empty(t, device.PrivPassword)
	assert.Equal(t, "u", device.Username, "the username is not a secret")
	assert.True(t, creds.hasCommunity() && creds.hasAuth() && creds.hasPriv())
	assert.True(t, creds.Alive())

	require.NoError(t, creds.use(context.Background(), func(s openSecrets) error {
		assert.Equal(t, openSecrets{community: "c0mm", authPassword: "authpass1", privPassword: "privpass1"}, s)
		return nil
	}))

	require.NoError(t, creds.Destroy())
	require.NoError(t, creds.Destroy(), "Destroy is idempotent")
	assert.False(t, creds.Alive())
}

func TestSealCredentialsFailureLeavesTheDeviceAndSealsNothing(t *testing.T) {
	device := &config.Device{Name: "r1", SNMPVersion: "3", Username: "u", AuthPassword: "a", PrivPassword: "p"}
	sealer := &afterFirstSealer{}

	creds, err := SealCredentials(context.Background(), sealer, device)
	assert.Nil(t, creds)
	var credErr *CredentialError
	require.ErrorAs(t, err, &credErr)
	assert.Equal(t, "r1", credErr.Device)
	assert.Equal(t, "priv_password", credErr.Field)
	var sealErr *credential.SealError
	assert.ErrorAs(t, err, &sealErr)
	assert.Contains(t, err.Error(), "priv_password for device r1")

	assert.Equal(t, "a", device.AuthPassword)
	assert.Equal(t, "p", device.PrivPassword)
	require.Len(t, sealer.sealed, 1)
	assert.False(t, sealer.sealed[0].IsAlive(), "the secret sealed before the failure is destroyed")
}

func TestNilCredentialsAreEmpty(t *testing.T) {
	var creds *Credentials
	assert.False(t, creds.Alive())
	assert.False(t, creds.hasCommunity() || creds.hasAuth() || creds.hasPriv())
	assert.NoError(t, creds.Destroy())
	require.NoError(t, creds.use(context.Background(), func(s openSecrets) error {
		assert.Equal(t, openSecrets{}, s)
		return nil
	}))
}

func TestNewClientRefusesWhenSealingFails(t *testing.T) {
	device := &config.Device{Name: "r1", Address: "127.0.0.1", SNMPVersion: "2c", Community: "public"}
	_, err := SealCredentials(context.Background(), refusingSealer{err: errors.New("down")}, device)
	var credErr *CredentialError
	require.ErrorAs(t, err, &credErr)
	assert.Equal(t, "public", device.Community)
}

// assertScrubbed fails if the connection still holds any credential between
// requests.
func assertScrubbed(t *testing.T, c *Client) {
	t.Helper()
	assert.Empty(t, c.conn.Community, "community left on the connection")
	if usm, ok := c.conn.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
		assert.Empty(t, usm.AuthenticationPassphrase, "auth passphrase left on the connection")
		assert.Empty(t, usm.PrivacyPassphrase, "priv passphrase left on the connection")
		assert.Nil(t, usm.SecretKey, "auth key left on the connection")
		assert.Nil(t, usm.PrivacyKey, "priv key left on the connection")
	}
}

func TestV2cPollUsesTheSealedCommunity(t *testing.T) {
	agent := newConfiguredAgent(t, func(a *testAgent) { a.community = "s3cret" }, systemGroup()...)
	device := agentDevice(t, agent)
	device.Community = "s3cret"

	client, err := NewClient(device, agentTimeout, 0)
	require.NoError(t, err)
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })
	assertScrubbed(t, client)

	res, err := client.Get(sysDescrOID)
	require.NoError(t, err)
	assert.NotNil(t, res.Value)
	assert.Zero(t, agent.rejected.Load())
	assertScrubbed(t, client)
}

func TestV2cWrongSealedCommunityIsRejected(t *testing.T) {
	agent := newConfiguredAgent(t, func(a *testAgent) { a.community = "s3cret" }, systemGroup()...)
	device := agentDevice(t, agent)
	device.Community = "wrong"

	client, err := NewClient(device, agentTimeout, 0)
	require.NoError(t, err)
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Get(sysDescrOID)
	assert.Error(t, err)
	assert.NotZero(t, agent.rejected.Load(), "the agent saw the wrong community")
	assertScrubbed(t, client)
}

func TestV3PollUsesTheSealedPassphrases(t *testing.T) {
	cases := []struct {
		name     string
		auth     gosnmp.SnmpV3AuthProtocol
		priv     gosnmp.SnmpV3PrivProtocol
		authName string
		privName string
	}{
		{"authNoPriv SHA256", gosnmp.SHA256, gosnmp.NoPriv, "SHA256", ""},
		{"authPriv SHA256 AES", gosnmp.SHA256, gosnmp.AES, "SHA256", "AES"},
		{"authPriv SHA512 AES256", gosnmp.SHA512, gosnmp.AES256, "SHA512", "AES256"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if reason := testsupport.SNMPv3PrivacyRefused(); tc.priv != gosnmp.NoPriv && reason != "" {
				t.Skip("sends the sealed privacy passphrase on the wire, and " + reason)
			}
			agent := newV3TestAgent(t, tc.auth, tc.priv, systemGroup()...)
			client, err := NewClient(v3AgentDevice(t, agent, tc.authName, tc.privName), agentTimeout, 0)
			require.NoError(t, err)
			require.NoError(t, client.Connect())
			t.Cleanup(func() { _ = client.Close() })

			for range 2 { // the second request runs with the engine already known
				res, err := client.Get(sysDescrOID)
				require.NoError(t, err)
				assert.NotNil(t, res.Value)
				assertScrubbed(t, client)
			}
			assert.Zero(t, agent.rejected.Load())
		})
	}
}

func TestV3WrongSealedPassphraseIsRejected(t *testing.T) {
	agent := newV3TestAgent(t, gosnmp.SHA256, gosnmp.NoPriv, systemGroup()...)
	device := v3AgentDevice(t, agent, "SHA256", "")
	device.AuthPassword = "not-the-passphrase"

	client, err := NewClient(device, agentTimeout, 0)
	require.NoError(t, err)
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.Get(sysDescrOID)
	assert.Error(t, err)
	assert.NotZero(t, agent.rejected.Load(), "the agent refused the passphrase")
	assertScrubbed(t, client)
}

func TestDestroyedCredentialsRefuseTheRequest(t *testing.T) {
	agent := newTestAgent(t, systemGroup()...)
	device := agentDevice(t, agent)
	creds, err := SealCredentials(context.Background(), credential.Default(), device)
	require.NoError(t, err)

	client, err := NewClientWithCredentials(device, creds, agentTimeout, 0)
	require.NoError(t, err)
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, creds.Destroy())

	before := agent.requests.Load()
	_, err = client.Get(sysDescrOID)
	var credErr *CredentialError
	require.ErrorAs(t, err, &credErr)
	assert.Equal(t, device.Name, credErr.Device)
	assert.ErrorIs(t, err, enclave.ErrEnclaveDestroyed)
	assert.Equal(t, before, agent.requests.Load(), "nothing is sent without credentials")
}

func TestNewClientWithCredentialsValidatesTheSealedDevice(t *testing.T) {
	device := &config.Device{Name: "r1", Address: "127.0.0.1", SNMPVersion: "2c"}
	_, err := NewClientWithCredentials(device, nil, agentTimeout, 0)
	assert.Error(t, err, "a v2c device needs a sealed community")

	v3 := &config.Device{Name: "r1", Address: "127.0.0.1", SNMPVersion: "3", Username: "u", AuthProtocol: "SHA"}
	_, err = NewClientWithCredentials(v3, nil, agentTimeout, 0)
	var cfgErr *V3ConfigError
	assert.ErrorAs(t, err, &cfgErr, "an auth protocol needs a sealed passphrase")
}
