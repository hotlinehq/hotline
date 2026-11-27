// Package vault is the proxy's in-memory vault (Peter, 2026-11-26): it
// makes the keys batches are signed with, each with a certificate, rotates
// them, and never writes a key anywhere. A key's certificate is signed by
// the key itself, or by an authority the config names (Peter, 2026-11-26).
// How a signature is written into a batch record is the ledger's business,
// not the vault's.
package vault

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// ErrNotACertificate is a text that is not a certificate for a key the
// vault can check a signature of, as the vault writes it: base64 of its DER
// bytes (the standard binary form of a certificate).
var ErrNotACertificate = errors.New("vault: not a certificate for an ed25519, ecdsa, rsa or ml-dsa key")

// ErrUnknownKeyType is a key type the vault does not make.
var ErrUnknownKeyType = errors.New("vault: unknown key type")

// ErrAuthorityCannotSign is an authority whose key cannot sign certificates.
var ErrAuthorityCannotSign = errors.New("vault: the authority's key cannot sign")

// The names of the signature algorithms, as a batch record writes them in
// front of a signature. Ed25519 and ML-DSA sign the digest as it is; ECDSA
// and RSA sign it as an already hashed SHA-256 value (Peter, 2026-11-26).
const (
	Ed25519        = "ed25519"
	ECDSASHA256    = "ecdsa-sha256"
	RSAPSSSHA256   = "rsa-pss-sha256"
	RSAPKCS1SHA256 = "rsa-pkcs1-sha256"
	MLDSA44        = "ml-dsa-44"
	MLDSA65        = "ml-dsa-65"
	MLDSA87        = "ml-dsa-87"
)

// HourlyKeyTypes are the types the proxy's own hourly keys can be: Ed25519
// by default, or ML-DSA by choice (Peter, 2026-11-26).
func HourlyKeyTypes() []string { return []string{Ed25519, MLDSA44, MLDSA65, MLDSA87} }

// HourlyKeys is how the vault makes its keys: their type, and the
// authority that signs their certificates. Without an authority each key
// signs its own.
type HourlyKeys struct {
	Type      string
	Authority *tls.Certificate
}

// same tells whether keys made under other are made as under s: the same
// type, and the same authority certificates, the ones above it too, so a
// renewed authority above it also makes a new key.
func (s HourlyKeys) same(other HourlyKeys) bool {
	if s.Type != other.Type || (s.Authority == nil) != (other.Authority == nil) {
		return false
	}
	return s.Authority == nil || slices.EqualFunc(s.Authority.Certificate, other.Authority.Certificate, bytes.Equal)
}

// Vault holds the key batches are signed with. It makes a key when it is
// made, at the proxy's start, a new one once the one in use is life old,
// and a new one at once when the setup changes, so no batch is signed
// under an authority after it was replaced. Its caller asks it once for
// each batch, so every copy of a batch is signed with the same key. It is
// not safe for use by two goroutines at once: its caller must ask it under
// a lock of its own.
type Vault struct {
	// service is the name each key's certificate gives the proxy.
	service string
	life    time.Duration
	setup   HourlyKeys
	signer  Signer
	made    time.Time
}

// New is a vault whose keys last life each, certified for the service,
// with its first key made at now under setup.
func New(service string, life time.Duration, setup HourlyKeys, now time.Time) *Vault {
	v := &Vault{service: service, life: life}
	v.make(setup, now)
	return v
}

// Signer is the signer to use at now, under setup: the one in use, or a
// new one, made at now, when the one in use is life old or was made under
// another setup.
func (v *Vault) Signer(setup HourlyKeys, now time.Time) Signer {
	if now.Sub(v.made) >= v.life || !setup.same(v.setup) {
		v.make(setup, now)
	}
	return v.signer
}

func (v *Vault) make(setup HourlyKeys, now time.Time) {
	v.setup, v.made = setup, now
	v.signer = newSigner(v.service, setup, now, v.life)
}

// Signer signs the digests of one batch with one key, and gives the
// certificate and the authorities to put beside the signatures. It prints
// as redacted, so a log line can never show the key.
type Signer struct {
	// Algorithm is the name of the signature algorithm, such as ed25519.
	Algorithm string
	// Certificate is the key's certificate as a batch record holds it:
	// base64 of its DER bytes.
	Certificate string
	// Authorities are the certificates above the key's, the one that
	// signed it first, each as Certificate is; none when the key signed
	// its own.
	Authorities []string
	sign        func(digests [][]byte) ([][]byte, error)
}

// Sign signs every digest, in one go, so a signer that calls out to
// another service later needs one call per batch. With an error, the
// signatures are not to be used.
func (s Signer) Sign(digests [][]byte) ([][]byte, error) { return s.sign(digests) }

// String hides the key when it is printed.
func (Signer) String() string { return "redacted" }

// GoString hides the key when it is printed with %#v.
func (Signer) GoString() string { return "redacted" }

// newSigner makes a new key of the setup's type from the system's random
// source, with a certificate naming the service, valid from made for life,
// and not an authority itself. The authority signs it, or the key itself.
// Certificate times are whole seconds, so the start is cut down to a whole
// second and the end is cut down plus one second, so the whole life is
// covered. A signer whose certificate cannot be made fails to sign, and
// says why.
func newSigner(service string, setup HourlyKeys, made time.Time, life time.Duration) Signer {
	key, err := newKey(setup.Type)
	if err != nil {
		return failing(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	end := made.Add(life)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: service},
		NotBefore:             made.Truncate(time.Second),
		NotAfter:              end.Truncate(time.Second).Add(time.Second),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	parent, parentKey := template, key.private
	var authorities []string
	if setup.Authority != nil {
		signs, ok := setup.Authority.PrivateKey.(crypto.Signer)
		if !ok {
			return failing(ErrAuthorityCannotSign)
		}
		parent, parentKey = setup.Authority.Leaf, signs
		for _, der := range setup.Authority.Certificate {
			authorities = append(authorities, base64.StdEncoding.EncodeToString(der))
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.private.Public(), parentKey)
	if err != nil {
		return failing(err)
	}
	return Signer{
		Algorithm: key.algorithm, Certificate: base64.StdEncoding.EncodeToString(der), Authorities: authorities,
		sign: func(digests [][]byte) ([][]byte, error) {
			out := make([][]byte, len(digests))
			errs := make([]error, len(digests))
			// Each signing keeps its own error, in errs: two batches may
			// be signed with this key at once.
			for i, d := range digests {
				out[i], errs[i] = key.private.Sign(rand.Reader, d, key.options)
			}
			return out, errors.Join(errs...)
		},
	}
}

// failing is a signer that cannot sign, for the reason err gives.
func failing(err error) Signer {
	return Signer{sign: func([][]byte) ([][]byte, error) { return nil, err }}
}

// hourlyKey is a new key with how it signs: its algorithm's name and the
// options its Sign takes.
type hourlyKey struct {
	private   crypto.Signer
	algorithm string
	options   crypto.SignerOpts
}

func newKey(keyType string) (hourlyKey, error) {
	switch keyType {
	case "", Ed25519:
		_, private, err := ed25519.GenerateKey(rand.Reader)
		return hourlyKey{private: private, algorithm: Ed25519, options: crypto.Hash(0)}, err
	case MLDSA44, MLDSA65, MLDSA87:
		private, err := mldsa.GenerateKey(mldsaParameters(keyType))
		return hourlyKey{private: private, algorithm: keyType, options: &mldsa.Options{}}, err
	}
	return hourlyKey{}, fmt.Errorf("%w %s", ErrUnknownKeyType, keyType)
}

func mldsaParameters(name string) mldsa.Parameters {
	switch name {
	case MLDSA44:
		return mldsa.MLDSA44()
	case MLDSA87:
		return mldsa.MLDSA87()
	}
	return mldsa.MLDSA65()
}

// Verify tells whether signature is the signature of digest, made with
// algorithm, by the key that certificate, as ParseCertificate gives it, is
// for. An algorithm that does not fit the key does not check out.
func Verify(certificate *x509.Certificate, algorithm string, digest, signature []byte) bool {
	switch public := certificate.PublicKey.(type) {
	case ed25519.PublicKey:
		return algorithm == Ed25519 && ed25519.Verify(public, digest, signature)
	case *ecdsa.PublicKey:
		return algorithm == ECDSASHA256 && len(digest) == sha256.Size && ecdsa.VerifyASN1(public, digest, signature)
	case *rsa.PublicKey:
		switch algorithm {
		case RSAPSSSHA256:
			return rsa.VerifyPSS(public, crypto.SHA256, digest, signature, nil) == nil
		case RSAPKCS1SHA256:
			return rsa.VerifyPKCS1v15(public, crypto.SHA256, digest, signature) == nil
		}
	case *mldsa.PublicKey:
		return strings.EqualFold(algorithm, public.Parameters().String()) && mldsa.Verify(public, digest, signature, &mldsa.Options{}) == nil
	}
	return false
}

// ParseCertificate reads a certificate as Signer writes it, and checks
// that it is for a key Verify can check.
func ParseCertificate(text string) (*x509.Certificate, error) {
	der, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, ErrNotACertificate
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrNotACertificate
	}
	switch certificate.PublicKey.(type) {
	case ed25519.PublicKey, *ecdsa.PublicKey, *rsa.PublicKey, *mldsa.PublicKey:
		return certificate, nil
	}
	return nil, ErrNotACertificate
}

// SelfSigned tells whether certificate is signed by its own key.
func SelfSigned(certificate *x509.Certificate) bool {
	return certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature) == nil
}
