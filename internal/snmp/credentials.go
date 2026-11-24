package snmp

import (
	"context"
	"unsafe"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
)

func init() {
	// Disable gosnmp's process-wide passphrase key cache, which keeps passphrases on the heap.
	gosnmp.PasswordCaching(false)
}

// Credentials are a device's SNMP secrets sealed in go-enclave memory, opened only for one request; the sealer destroys them.
type Credentials struct {
	community    *credential.Secret
	authPassword *credential.Secret
	privPassword *credential.Secret
}

// SealCredentials seals the device's secrets and removes them from it, so that
// the device can be kept without them. On failure nothing stays sealed and the
// device is left as it was.
func SealCredentials(ctx context.Context, sealer credential.Sealer, device *config.Device) (*Credentials, error) {
	creds := &Credentials{}
	fields := []struct {
		value *string
		into  **credential.Secret
		name  string
	}{
		{&device.Community, &creds.community, "community"},
		{&device.AuthPassword, &creds.authPassword, "auth_password"},
		{&device.PrivPassword, &creds.privPassword, "priv_password"},
	}
	// Refuse at startup a device whose open could never fit under RLIMIT_MEMLOCK.
	budget, err := lockedBudget()
	if err != nil {
		return nil, &CredentialError{Device: device.Name, Err: err}
	}
	var cost int64
	for _, f := range fields {
		cost += credential.LockedCost(len(*f.value))
	}
	release, err := budget.Acquire(ctx, cost)
	if err != nil {
		return nil, &CredentialError{Device: device.Name, Err: err}
	}
	defer release()
	for _, f := range fields {
		secret, err := credential.SealString(ctx, sealer, *f.value)
		if err != nil {
			_ = creds.Destroy()
			return nil, &CredentialError{Device: device.Name, Field: f.name, Err: err}
		}
		*f.into = secret
	}
	for _, f := range fields {
		*f.value = ""
	}
	return creds, nil
}

// hasCommunity, hasAuth and hasPriv report which secrets were supplied.
func (c *Credentials) hasCommunity() bool { return c != nil && c.community != nil }
func (c *Credentials) hasAuth() bool      { return c != nil && c.authPassword != nil }
func (c *Credentials) hasPriv() bool      { return c != nil && c.privPassword != nil }

// Alive reports whether any of the secrets is still sealed.
func (c *Credentials) Alive() bool {
	return c != nil && (c.community.Alive() || c.authPassword.Alive() || c.privPassword.Alive())
}

// Destroy wipes every secret. It is idempotent and nil-safe.
func (c *Credentials) Destroy() error {
	if c == nil {
		return nil
	}
	var first error
	for _, s := range []*credential.Secret{c.community, c.authPassword, c.privPassword} {
		if err := s.Destroy(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// openSecrets is the plaintext of a device's credentials, valid only inside
// Credentials.use.
type openSecrets struct {
	community    string
	authPassword string
	privPassword string
}

// use opens every secret and calls fn with views valid only until fn returns; waits for locked memory until ctx ends.
func (c *Credentials) use(ctx context.Context, fn func(openSecrets) error) error {
	if c == nil {
		return fn(openSecrets{})
	}
	budget, err := lockedBudget()
	if err != nil {
		return err
	}
	secrets := []*credential.Secret{c.community, c.authPassword, c.privPassword}
	return credential.UseAll(ctx, budget, secrets, func(p [][]byte) error {
		return fn(openSecrets{
			community:    view(p[0]),
			authPassword: view(p[1]),
			privPassword: view(p[2]),
		})
	})
}

// lockedBudget is the budget every seal and open of a device's credentials
// waits on. It is a variable so tests can shrink it.
var lockedBudget = credential.ProcessBudget

// lockedCost is the locked memory, in bytes, that opening c holds.
func (c *Credentials) lockedCost() int64 {
	if c == nil {
		return 0
	}
	return c.community.LockedCost() + c.authPassword.LockedCost() + c.privPassword.LockedCost()
}

// view is a string over b's memory, without copying it onto the heap.
func view(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b)) // #nosec G103 -- b is a locked enclave view; the string never outlives it
}
