package credential

import (
	"context"
	"errors"
	"testing"

	enclave "github.com/jeremyhahn/go-enclave"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusingSealer is a sealer whose provider has failed.
type refusingSealer struct{ err error }

func (r refusingSealer) Seal(context.Context, []byte) (enclave.Sealed, error) { return nil, r.err }

func TestSealWipesPlaintextAndUseRecoversIt(t *testing.T) {
	plaintext := []byte("s3cret-community")
	secret, err := Seal(context.Background(), Default(), plaintext)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secret.Destroy() })

	assert.Equal(t, make([]byte, len(plaintext)), plaintext, "the caller's copy is wiped")
	assert.True(t, secret.Alive())

	var seen string
	require.NoError(t, secret.Use(context.Background(), func(b []byte) error {
		seen = string(b)
		return nil
	}))
	assert.Equal(t, "s3cret-community", seen)
}

func TestSealStringRoundTrips(t *testing.T) {
	secret, err := SealString(context.Background(), Default(), "authpass1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = secret.Destroy() })

	require.NoError(t, secret.Use(context.Background(), func(b []byte) error {
		assert.Equal(t, []byte("authpass1"), b)
		return nil
	}))
}

func TestEmptyPlaintextIsTheAbsentCredential(t *testing.T) {
	secret, err := SealString(context.Background(), Default(), "")
	require.NoError(t, err)
	assert.Nil(t, secret)
	assert.False(t, secret.Alive())
	assert.NoError(t, secret.Destroy())

	called := false
	require.NoError(t, secret.Use(context.Background(), func(b []byte) error {
		called = true
		assert.Empty(t, b)
		return nil
	}))
	assert.True(t, called)
}

func TestDestroyedSecretCannotBeOpened(t *testing.T) {
	secret, err := SealString(context.Background(), Default(), "private")
	require.NoError(t, err)

	require.NoError(t, secret.Destroy())
	require.NoError(t, secret.Destroy(), "Destroy is idempotent")
	assert.False(t, secret.Alive())

	err = secret.Use(context.Background(), func([]byte) error {
		t.Fatal("a destroyed secret must not be opened")
		return nil
	})
	var openErr *OpenError
	require.ErrorAs(t, err, &openErr)
	assert.ErrorIs(t, err, enclave.ErrEnclaveDestroyed)
	assert.Contains(t, err.Error(), "credential: open")
}

func TestUseReturnsTheFunctionsError(t *testing.T) {
	secret, err := SealString(context.Background(), Default(), "x")
	require.NoError(t, err)
	t.Cleanup(func() { _ = secret.Destroy() })

	boom := errors.New("boom")
	assert.ErrorIs(t, secret.Use(context.Background(), func([]byte) error { return boom }), boom)
}

func TestSealFailureWipesAndIsTyped(t *testing.T) {
	refused := errors.New("provider down")
	plaintext := []byte("privpass")

	secret, err := Seal(context.Background(), refusingSealer{err: refused}, plaintext)
	assert.Nil(t, secret)
	var sealErr *SealError
	require.ErrorAs(t, err, &sealErr)
	assert.ErrorIs(t, err, refused)
	assert.Contains(t, err.Error(), "credential: seal")
	assert.Equal(t, make([]byte, len(plaintext)), plaintext)
}

func TestSealWithoutSealerIsRefused(t *testing.T) {
	plaintext := []byte("community")
	_, err := Seal(context.Background(), nil, plaintext)
	assert.ErrorIs(t, err, ErrNoSealer)
	assert.Equal(t, make([]byte, len(plaintext)), plaintext)
}

func TestFailedDefaultSealerReportsAndWipes(t *testing.T) {
	refused := errors.New("no sealer")
	plaintext := []byte("community")
	_, err := Seal(context.Background(), failedSealer{err: refused}, plaintext)
	assert.ErrorIs(t, err, refused)
	assert.Equal(t, make([]byte, len(plaintext)), plaintext)
}

func TestDefaultIsOneSealer(t *testing.T) {
	assert.Same(t, Default(), Default())
}
