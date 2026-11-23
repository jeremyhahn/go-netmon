package poller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	enclave "github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// Credentials no test or real configuration would contain by accident, so
// finding one of them anywhere means it leaked.
const (
	sealedCommunity = "c0mmunity-under-seal"
	sealedAuthPass  = "auth-passphrase-under-seal"
	sealedPrivPass  = "priv-passphrase-under-seal"
	sealedV3User    = "netmon-v3"
)

// countingSealer is the default sealer, counted, so a test can see the
// poller used the one it was given.
type countingSealer struct{ seals atomic.Int64 }

func (s *countingSealer) Seal(ctx context.Context, b []byte) (enclave.Sealed, error) {
	s.seals.Add(1)
	return credential.Default().Seal(ctx, b)
}

// refusingSealer is a sealer whose provider has failed.
type refusingSealer struct{}

func (refusingSealer) Seal(_ context.Context, b []byte) (enclave.Sealed, error) {
	enclave.Wipe(b)
	return nil, errors.New("provider down")
}

func v2cSealedAgent(t *testing.T, sysName string) *testsupport.SNMPAgent {
	t.Helper()
	agent := pollingAgent(t, sysName)
	agent.RequireCommunity(sealedCommunity)
	return agent
}

// sealedV3AuthPriv reports whether the v3 device runs at authPriv, which
// seals a privacy passphrase as well. Under fips140=only no SNMPv3 privacy
// protocol can run (see testsupport.SNMPv3PrivacyRefused), and the device
// runs at authNoPriv instead.
func sealedV3AuthPriv() bool { return testsupport.SNMPv3PrivacyRefused() == "" }

func v3SealedAgent(t *testing.T, sysName string) *testsupport.SNMPAgent {
	t.Helper()
	user := testsupport.SNMPv3User{
		Name:           sealedV3User,
		AuthProtocol:   gosnmp.SHA512,
		AuthPassphrase: sealedAuthPass,
		PrivProtocol:   gosnmp.NoPriv,
	}
	if sealedV3AuthPriv() {
		user.PrivProtocol = gosnmp.AES
		user.PrivPassphrase = sealedPrivPass
	}
	agent, err := testsupport.NewSNMPv3Agent(user,
		testsupport.OctetString(sysDescrOID, "go-netmon in-process test agent"),
		testsupport.TimeTicks(sysUpTimeOID, 4242),
		testsupport.OctetString(sysNameOID, sysName),
	)
	if err != nil {
		t.Fatalf("start snmpv3 agent: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent
}

func v2cSealedDevice(name string, agent *testsupport.SNMPAgent) config.Device {
	device := agentDevice(name, agent)
	device.Community = sealedCommunity
	device.PollInterval = 3600 // one poll is all these tests need
	return device
}

func v3SealedDevice(name string, agent *testsupport.SNMPAgent) config.Device {
	device := config.Device{
		Name:         name,
		Address:      agent.Addr(),
		SNMPVersion:  "3",
		Username:     sealedV3User,
		AuthProtocol: "SHA512",
		AuthPassword: sealedAuthPass,
		PollInterval: 3600,
		Enabled:      true,
	}
	if sealedV3AuthPriv() {
		device.PrivProtocol = "AES"
		device.PrivPassword = sealedPrivPass
	}
	return device
}

// liveCreds returns the sealed credentials of the named device's worker.
func liveCreds(t *testing.T, p *Poller, name string) *snmp.Credentials {
	t.Helper()
	p.mu.RLock()
	defer p.mu.RUnlock()
	w := p.liveWorker(name)
	if w == nil {
		t.Fatalf("no worker for %s", name)
	}
	return w.creds
}

func TestPollerPollsV2cAndV3WithSealedCredentials(t *testing.T) {
	t.Parallel()

	v2c := v2cSealedAgent(t, "v2c-device")
	v3 := v3SealedAgent(t, "v3-device")

	cfg := getTestConfig(t)
	cfg.Devices = []config.Device{
		v2cSealedDevice("v2c-device", v2c),
		v3SealedDevice("v3-device", v3),
	}

	sealer := &countingSealer{}
	p := New(cfg, nil, WithSealer(sealer))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The community, the v3 auth passphrase and, at authPriv, the privacy
	// passphrase.
	want := int64(2)
	if sealedV3AuthPriv() {
		want = 3
	}
	if got := sealer.seals.Load(); got != want {
		t.Errorf("the injected sealer sealed %d secrets, want %d", got, want)
	}

	results := awaitPollResults(t, p, []string{"v2c-device", "v3-device"}, 10*time.Second)
	for name, result := range results {
		if result.Error != nil {
			t.Errorf("%s: poll with sealed credentials failed: %v", name, result.Error)
			continue
		}
		if got := sysNameFrom(t, result); got != name {
			t.Errorf("%s read sysName %q", name, got)
		}
	}
	if v2c.Rejected() != 0 || v3.Rejected() != 0 {
		t.Errorf("agents rejected %d (v2c) and %d (v3) requests: the sealed credentials did not reach the wire intact",
			v2c.Rejected(), v3.Rejected())
	}

	// What the control API sees of a device carries no credential.
	for name, status := range p.GetWorkerStatus() {
		if d := status.Device; d.Community != "" || d.AuthPassword != "" || d.PrivPassword != "" {
			t.Errorf("worker status for %s exposes a credential", name)
		}
	}

	creds := []*snmp.Credentials{liveCreds(t, p, "v2c-device"), liveCreds(t, p, "v3-device")}
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for i, c := range creds {
		if c.Alive() {
			t.Errorf("credentials %d outlived Stop", i)
		}
	}

	// After Stop no goroutine is left writing, so the whole poller can be
	// walked: no string or byte slice reachable from it may hold a secret.
	assertNoPlaintext(t, p, sealedCommunity, sealedAuthPass, sealedPrivPass)
}

func TestRemovingADeviceDestroysItsSealedCredentials(t *testing.T) {
	t.Parallel()

	agent := v2cSealedAgent(t, "removed")
	cfg := getTestConfig(t)
	cfg.Devices = nil
	p := New(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := v2cSealedDevice("removed", agent)
	if err := p.AddDevice(ctx, &device); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	if device.Community != sealedCommunity {
		t.Error("AddDevice modified the caller's device")
	}

	awaitPollResults(t, p, []string{"removed"}, 10*time.Second)
	creds := liveCreds(t, p, "removed")
	if !creds.Alive() {
		t.Fatal("a running device's credentials are not sealed")
	}

	if err := p.RemoveDevice("removed"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}
	if creds.Alive() {
		t.Error("RemoveDevice left the device's credentials sealed in memory")
	}
}

func TestAddDeviceFailsWhenItsCredentialsCannotBeSealed(t *testing.T) {
	t.Parallel()

	agent := pollingAgent(t, "unsealable")
	cfg := getTestConfig(t)
	cfg.Devices = nil
	p := New(cfg, nil, WithSealer(refusingSealer{}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	device := agentDevice("unsealable", agent)
	err := p.AddDevice(ctx, &device)
	var credErr *snmp.CredentialError
	if !errors.As(err, &credErr) {
		t.Fatalf("AddDevice = %v, want a *snmp.CredentialError", err)
	}
	if _, running := p.GetWorkerStatus()["unsealable"]; running {
		t.Error("a device whose credentials could not be sealed was started anyway")
	}
}

func TestWithNilSealerKeepsTheDefault(t *testing.T) {
	p := New(getTestConfig(t), nil, WithSealer(nil))
	if p.sealer != credential.Default() {
		t.Error("WithSealer(nil) replaced the default sealer")
	}
}

// assertNoPlaintext walks everything reachable from root and fails on any
// string or byte slice containing one of secrets. It does not descend into
// a *credential.Secret: what is behind that is sealed, and opening its
// memory by reflection is exactly what the enclave exists to prevent.
func assertNoPlaintext(t *testing.T, root any, secrets ...string) {
	t.Helper()
	secretType := reflect.TypeOf((*credential.Secret)(nil))
	seen := make(map[uintptr]bool)

	var walk func(v reflect.Value, path string)
	check := func(s, path string) {
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Errorf("plaintext credential reachable at %s", path)
			}
		}
	}
	walk = func(v reflect.Value, path string) {
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.String:
			check(v.String(), path)
		case reflect.Pointer:
			if v.IsNil() || v.Type() == secretType || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
			walk(v.Elem(), path)
		case reflect.Interface:
			walk(v.Elem(), path)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Slice:
			if v.IsNil() {
				return
			}
			if v.Type().Elem().Kind() == reflect.Uint8 {
				b := make([]byte, v.Len())
				for i := range b {
					b[i] = byte(v.Index(i).Uint())
				}
				check(string(b), path)
				return
			}
			if seen[v.Pointer()] && v.Len() > 0 {
				return
			}
			seen[v.Pointer()] = true
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), path+"[]")
			}
		case reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), path+"[]")
			}
		case reflect.Map:
			if v.IsNil() || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
			iter := v.MapRange()
			for iter.Next() {
				walk(iter.Key(), path+"{key}")
				walk(iter.Value(), path+"{}")
			}
		}
	}
	walk(reflect.ValueOf(root), reflect.TypeOf(root).String())
}
