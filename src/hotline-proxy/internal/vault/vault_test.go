package vault_test

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-proxy/internal/vault"
)

func TestVault(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Vault Suite")
}

var _ = Describe("The vault", func() {
	sut := vaultSUT{}

	It("signs two batches at once with the same key, each signature checking out", func() {
		sut.forVault()
		Expect(sut.SignaturesCheckingOutOfBatchesSignedAtOnce(8)).To(Equal(16))
	})

	It("never prints its private part", func() {
		sut.forVault()
		signer := sut.Signer()
		for _, text := range []string{fmt.Sprint(signer), fmt.Sprintf("%v %+v %#v %s", signer, signer, signer, signer)} {
			Expect(text).NotTo(ContainSubstring(signer.Certificate))
			Expect(text).To(ContainSubstring("redacted"))
		}
	})

	It("gives the same key until it is an hour old, then a new one", func() {
		sut.forVault()
		first := sut.Signer()
		Expect(sut.SignerAfter(59 * time.Minute).Certificate).To(Equal(first.Certificate))
		Expect(sut.SignerAfter(time.Hour).Certificate).NotTo(Equal(first.Certificate))
	})

	It("gives a new key at once when the key type changes", func() {
		sut.forVault()
		first := sut.Signer()
		next := sut.SignerOfTypeAfter(vault.MLDSA44, time.Minute)
		Expect(next.Certificate).NotTo(Equal(first.Certificate))
		Expect(next.Algorithm).To(Equal("ml-dsa-44"))
	})

	It("cannot sign with a key type it does not know, and says so", func() {
		sut.forVaultOfKeyType("dsa")
		Expect(sut.SignOneDigest()).To(MatchError(ContainSubstring("unknown key type dsa")))
	})
})

var _ = Describe("Reading a batch certificate", func() {
	sut := certificateSUT{}

	It("names the service and covers the key's whole hour, in whole seconds", func() {
		made := time.Date(2026, 11, 26, 8, 0, 0, 500_000_000, time.UTC)
		sut.forBatchCertificateMadeAt(made)
		certificate := sut.Read()
		Expect(certificate.Subject.CommonName).To(Equal("hotline-proxy"))
		Expect(certificate.NotBefore).To(Equal(made.Truncate(time.Second)))
		Expect(certificate.NotAfter).To(Equal(made.Add(time.Hour).Truncate(time.Second).Add(time.Second)))
		Expect(certificate.IsCA).To(BeFalse())
		Expect(vault.SelfSigned(certificate)).To(BeTrue())
	})

	It("refuses text that is not base64", func() {
		sut.forText("!!")
		Expect(sut.ReadError()).To(MatchError(vault.ErrNotACertificate))
	})

	It("refuses text that is not a certificate", func() {
		sut.forText(base64.StdEncoding.EncodeToString([]byte("not a certificate")))
		Expect(sut.ReadError()).To(MatchError(vault.ErrNotACertificate))
	})

	It("tells a certificate another key signed from a self-signed one", func() {
		sut.forCertificateSignedByAnotherKey()
		Expect(vault.SelfSigned(sut.Read())).To(BeFalse())
	})

	It("checks an ECDSA signature only under its own algorithm's name", func() {
		sut.forECDSACertificate()
		Expect(sut.SignatureChecksOutAs("ecdsa-sha256")).To(BeTrue())
		Expect(sut.SignatureChecksOutAs("ed25519")).To(BeFalse())
	})

	It("checks an RSA signature only under an RSA algorithm's name", func() {
		sut.forRSACertificate()
		Expect(sut.SignatureChecksOutAs("rsa-pkcs1-sha256")).To(BeTrue())
		Expect(sut.SignatureChecksOutAs("ed25519")).To(BeFalse())
	})

	It("refuses a certificate for a key that cannot sign, such as an X25519 one", func() {
		sut.forX25519Certificate()
		Expect(sut.ReadError()).To(MatchError(vault.ErrNotACertificate))
	})

	Context("under an authority it cannot use", func() {
		sut := authoritySUT{}

		It("cannot sign when the authority's key cannot sign", func() {
			sut.forAuthorityHoldingKey("not a key")
			Expect(sut.SignOneDigest()).To(MatchError(vault.ErrAuthorityCannotSign))
		})

		It("cannot sign when the authority's key is not its certificate's", func() {
			sut.forAuthorityWithAnotherKey()
			Expect(sut.SignOneDigest()).To(HaveOccurred())
		})

		It("signs when the authority is whole, with the authority beside the certificate", func() {
			sut.forAuthority()
			Expect(sut.Signer().Authorities).To(Equal([]string{sut.AuthorityCertificate()}))
			Expect(sut.SignOneDigest()).To(Succeed())
		})
	})
})

// vaultSUT is one vault of hourly keys, started at a fixed time.
type vaultSUT struct {
	vault *vault.Vault
	start time.Time
	keys  vault.HourlyKeys
}

// forVault is a vault of the default key type.
func (s *vaultSUT) forVault() { s.forVaultOfKeyType("") }

// forVaultOfKeyType is a vault of keys of type keyType.
func (s *vaultSUT) forVaultOfKeyType(keyType string) {
	s.start = time.UnixMicro(1_700_000_000_000_000)
	s.keys = vault.HourlyKeys{Type: keyType}
	s.vault = vault.New("hotline-proxy", time.Hour, s.keys, s.start)
}

// Signer is the signer the vault gives at its start.
func (s *vaultSUT) Signer() vault.Signer { return s.SignerAfter(0) }

// SignerAfter is the signer the vault gives once d has passed.
func (s *vaultSUT) SignerAfter(d time.Duration) vault.Signer {
	return s.vault.Signer(s.keys, s.start.Add(d))
}

// SignerOfTypeAfter is the signer the vault gives once d has passed, when
// the keys are now of type keyType.
func (s *vaultSUT) SignerOfTypeAfter(keyType string, d time.Duration) vault.Signer {
	s.keys.Type = keyType
	return s.SignerAfter(d)
}

// SignOneDigest signs one digest with the signer of the vault's start.
func (s *vaultSUT) SignOneDigest() error {
	_, err := s.Signer().Sign([][]byte{make([]byte, 32)})
	return err
}

// SignaturesCheckingOutOfBatchesSignedAtOnce signs n batches of two
// digests each, all at the same time with the same signer, and counts the
// signatures that check out against the signer's certificate.
func (s *vaultSUT) SignaturesCheckingOutOfBatchesSignedAtOnce(n byte) int {
	signer := s.Signer()
	certificate, err := vault.ParseCertificate(signer.Certificate)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	var good atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			defer GinkgoRecover()
			digest := make([]byte, 32)
			digest[0] = i
			signatures, err := signer.Sign([][]byte{digest, digest})
			Expect(err).NotTo(HaveOccurred())
			for _, signature := range signatures {
				if vault.Verify(certificate, signer.Algorithm, digest, signature) {
					good.Add(1)
				}
			}
		})
	}
	wg.Wait()
	return int(good.Load())
}

// certificateSUT is one certificate as text, and the key that signed it
// when the test made it.
type certificateSUT struct {
	text string
	sign func(digest []byte) []byte
}

// forText is text that may or may not be a certificate.
func (s *certificateSUT) forText(text string) { s.text = text }

// forBatchCertificateMadeAt is the certificate of a vault's key made at
// made.
func (s *certificateSUT) forBatchCertificateMadeAt(made time.Time) {
	s.text = vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, made).Signer(vault.HourlyKeys{}, made).Certificate
}

// forCertificateSignedByAnotherKey is an Ed25519 certificate that a key
// other than its own signed.
func (s *certificateSUT) forCertificateSignedByAnotherKey() {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	_, other, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.text = base64.StdEncoding.EncodeToString(selfMade(public, other))
}

// forECDSACertificate is a self-signed P-256 certificate, and its key
// signs.
func (s *certificateSUT) forECDSACertificate() {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.text = base64.StdEncoding.EncodeToString(selfMade(&private.PublicKey, private))
	s.sign = func(digest []byte) []byte {
		signature, err := ecdsa.SignASN1(rand.Reader, private, digest)
		Expect(err).NotTo(HaveOccurred())
		return signature
	}
}

// forRSACertificate is a self-signed RSA certificate, and its key signs.
func (s *certificateSUT) forRSACertificate() {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.text = base64.StdEncoding.EncodeToString(selfMade(&private.PublicKey, private))
	s.sign = func(digest []byte) []byte {
		signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest)
		Expect(err).NotTo(HaveOccurred())
		return signature
	}
}

// forX25519Certificate is a certificate for an X25519 key. Go makes no
// X25519 certificate, so an Ed25519 one has its key's name changed: both
// keys are 32 bytes written the same way.
func (s *certificateSUT) forX25519Certificate() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	der := selfMade(public, private)
	ed25519Name, x25519Name := []byte{0x06, 0x03, 0x2b, 0x65, 0x70}, []byte{0x06, 0x03, 0x2b, 0x65, 0x6e}
	key := bytes.Index(der, append(ed25519Name, 0x03, 0x21))
	Expect(key).WithOffset(1).To(BeNumerically(">", 0))
	copy(der[key:], x25519Name)
	s.text = base64.StdEncoding.EncodeToString(der)
}

// Read reads the certificate, failing the test when it cannot.
func (s *certificateSUT) Read() *x509.Certificate {
	certificate, err := vault.ParseCertificate(s.text)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	return certificate
}

// ReadError is why the text cannot be read as a certificate.
func (s *certificateSUT) ReadError() error {
	_, err := vault.ParseCertificate(s.text)
	return err
}

// SignatureChecksOutAs signs a digest with the certificate's key, and
// says whether the signature checks out under the algorithm's name.
func (s *certificateSUT) SignatureChecksOutAs(algorithm string) bool {
	digest := make([]byte, 32)
	return vault.Verify(s.Read(), algorithm, digest, s.sign(digest))
}

// selfMade is a certificate of public, valid for an hour from now, that
// signer signed.
func selfMade(public, signer any) []byte {
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, signer)
	Expect(err).WithOffset(2).NotTo(HaveOccurred())
	return der
}

// authoritySUT is a vault whose keys a certificate authority signs, and
// that authority, whole or broken.
type authoritySUT struct {
	authority *tls.Certificate
	now       time.Time
}

// forAuthority is a whole authority: its certificate and its own key.
func (s *authoritySUT) forAuthority() { s.forAuthorityHoldingKey(nil) }

// forAuthorityWithAnotherKey is an authority whose key is not its
// certificate's.
func (s *authoritySUT) forAuthorityWithAnotherKey() {
	_, other, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	s.forAuthorityHoldingKey(other)
}

// forAuthorityHoldingKey is an authority that holds key as its private
// key; nil holds its own.
func (s *authoritySUT) forAuthorityHoldingKey(key any) {
	s.now = time.UnixMicro(1_700_000_000_000_000)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: s.now, NotAfter: s.now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	leaf, err := x509.ParseCertificate(der)
	Expect(err).WithOffset(1).NotTo(HaveOccurred())
	if key == nil {
		key = private
	}
	s.authority = &tls.Certificate{Certificate: [][]byte{der}, Leaf: leaf, PrivateKey: key}
}

// Signer is the signer of a new vault under the authority.
func (s *authoritySUT) Signer() vault.Signer {
	keys := vault.HourlyKeys{Authority: s.authority}
	return vault.New("hotline-proxy", time.Hour, keys, s.now).Signer(keys, s.now)
}

// SignOneDigest signs one digest under the authority.
func (s *authoritySUT) SignOneDigest() error {
	_, err := s.Signer().Sign([][]byte{make([]byte, 32)})
	return err
}

// AuthorityCertificate is the authority's certificate as text.
func (s *authoritySUT) AuthorityCertificate() string {
	return base64.StdEncoding.EncodeToString(s.authority.Certificate[0])
}
