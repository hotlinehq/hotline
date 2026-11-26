// Package vault is the proxy's in-memory vault (Peter, 2026-11-26): it
// makes the keys batches are signed with, each with a certificate it signs
// itself, rotates them, and never writes a key anywhere. How a signature
// is written into a batch record is the ledger's business, not the
// vault's.
package vault

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"time"
)

// ErrNotACertificate is a text that is not a certificate for an Ed25519
// key as the vault writes it: base64 of its DER bytes (the standard
// binary form of a certificate).
var ErrNotACertificate = errors.New("vault: not a certificate for an ed25519 key")

// ErrNotSelfSigned is a certificate that its own key did not sign.
var ErrNotSelfSigned = errors.New("vault: the certificate is not signed by its own key")

// Vault holds the key batches are signed with. It makes a key when it is
// made, at the proxy's start, and a new one once the one in use is life
// old. Its caller asks it once for each batch, so every copy of a batch is
// signed with the same key. It is not safe for use by two goroutines at
// once: its caller must ask it under a lock of its own.
type Vault struct {
	// service is the name each key's certificate gives the proxy.
	service string
	life    time.Duration
	key     Key
	made    time.Time
}

// New is a vault whose keys last life each, certified for the service,
// with its first key made at now.
func New(service string, life time.Duration, now time.Time) *Vault {
	return &Vault{service: service, life: life, key: NewKey(service, now, life), made: now}
}

// Key is the key in use at now: a new one, made at now, when the one in
// use is life old.
func (v *Vault) Key(now time.Time) Key {
	if now.Sub(v.made) >= v.life {
		v.key, v.made = NewKey(v.service, now, v.life), now
	}
	return v.key
}

// Key is a signing key the vault makes in memory and never writes down,
// with its certificate. It prints as redacted, so a log line can never
// show it.
type Key struct {
	private ed25519.PrivateKey
	// Certificate is the key's certificate as a batch record holds it:
	// base64 of its DER bytes (the standard binary form of a certificate).
	Certificate string
}

// NewKey makes a new key from the system's random source, with a
// certificate it signs itself (Peter, 2026-11-26): naming the service,
// valid from made for life. Certificate times are whole seconds, so the
// start is cut down to a whole second and the end is cut down plus one
// second, so the whole life is covered.
// A certificate from a trusted source can take its place later.
func NewKey(service string, made time.Time, life time.Duration) Key {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	end := made.Add(life)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: service},
		NotBefore:    made.Truncate(time.Second),
		NotAfter:     end.Truncate(time.Second).Add(time.Second),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	// An Ed25519 key and a template with a serial and a name always make
	// a certificate.
	der, _ := x509.CreateCertificate(rand.Reader, template, template, public, private)
	return Key{private: private, Certificate: base64.StdEncoding.EncodeToString(der)}
}

// Sign gives the raw Ed25519 signature of digest.
func (k Key) Sign(digest []byte) []byte { return ed25519.Sign(k.private, digest) }

// Verify tells whether signature is the signature of digest by the key
// that certificate, as ParseCertificate gives it, is for.
func Verify(certificate *x509.Certificate, digest, signature []byte) bool {
	public, _ := certificate.PublicKey.(ed25519.PublicKey)
	return ed25519.Verify(public, digest, signature)
}

// String hides the key when it is printed.
func (Key) String() string { return "redacted" }

// GoString hides the key when it is printed with %#v.
func (Key) GoString() string { return "redacted" }

// ParseCertificate reads a key's certificate, as Key.Certificate writes
// it, and checks that it is for an Ed25519 key and signed by that key
// itself.
func ParseCertificate(text string) (*x509.Certificate, error) {
	der, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, ErrNotACertificate
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrNotACertificate
	}
	if _, ok := certificate.PublicKey.(ed25519.PublicKey); !ok {
		return nil, ErrNotACertificate
	}
	if certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature) != nil {
		return nil, ErrNotSelfSigned
	}
	return certificate, nil
}
