package vault_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math/big"
	"sync"
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
	start := time.UnixMicro(1_700_000_000_000_000)

	It("signs two batches at once with the same key, each signature checking out", func() {
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, start).Signer(vault.HourlyKeys{}, start)
		certificate, err := vault.ParseCertificate(signer.Certificate)
		Expect(err).NotTo(HaveOccurred())
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				defer GinkgoRecover()
				digest := make([]byte, 32)
				digest[0] = byte(i)
				signatures, err := signer.Sign([][]byte{digest, digest})
				Expect(err).NotTo(HaveOccurred())
				for _, signature := range signatures {
					Expect(vault.Verify(certificate, signer.Algorithm, digest, signature)).To(BeTrue())
				}
			})
		}
		wg.Wait()
	})
	It("never prints its private part", func() {
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, start).Signer(vault.HourlyKeys{}, start)
		for _, text := range []string{fmt.Sprint(signer), fmt.Sprintf("%v %+v %#v %s", signer, signer, signer, signer)} {
			Expect(text).NotTo(ContainSubstring(signer.Certificate))
			Expect(text).To(ContainSubstring("redacted"))
		}
	})

	It("gives the same key until it is an hour old, then a new one", func() {
		v := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, start)
		first := v.Signer(vault.HourlyKeys{}, start)
		Expect(v.Signer(vault.HourlyKeys{}, start.Add(59*time.Minute)).Certificate).To(Equal(first.Certificate))
		Expect(v.Signer(vault.HourlyKeys{}, start.Add(time.Hour)).Certificate).NotTo(Equal(first.Certificate))
	})

	It("gives a new key at once when the key type changes", func() {
		v := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, start)
		first := v.Signer(vault.HourlyKeys{}, start)
		next := v.Signer(vault.HourlyKeys{Type: vault.MLDSA44}, start.Add(time.Minute))
		Expect(next.Certificate).NotTo(Equal(first.Certificate))
		Expect(next.Algorithm).To(Equal("ml-dsa-44"))
	})

	It("cannot sign with a key type it does not know, and says so", func() {
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{Type: "dsa"}, start).Signer(vault.HourlyKeys{Type: "dsa"}, start)
		_, err := signer.Sign([][]byte{make([]byte, 32)})
		Expect(err).To(MatchError(ContainSubstring("unknown key type dsa")))
	})
})

var _ = Describe("Reading a batch certificate", func() {
	It("names the service and covers the key's whole hour, in whole seconds", func() {
		made := time.Date(2026, 11, 26, 8, 0, 0, 500_000_000, time.UTC)
		signer := vault.New("hotline-proxy", time.Hour, vault.HourlyKeys{}, made).Signer(vault.HourlyKeys{}, made)
		certificate, err := vault.ParseCertificate(signer.Certificate)
		Expect(err).NotTo(HaveOccurred())
		Expect(certificate.Subject.CommonName).To(Equal("hotline-proxy"))
		Expect(certificate.NotBefore).To(Equal(made.Truncate(time.Second)))
		Expect(certificate.NotAfter).To(Equal(made.Add(time.Hour).Truncate(time.Second).Add(time.Second)))
		Expect(certificate.IsCA).To(BeFalse())
		Expect(vault.SelfSigned(certificate)).To(BeTrue())
	})

	It("refuses text that is not base64", func() {
		_, err := vault.ParseCertificate("!!")
		Expect(err).To(MatchError(vault.ErrNotACertificate))
	})

	It("refuses text that is not a certificate", func() {
		_, err := vault.ParseCertificate(base64.StdEncoding.EncodeToString([]byte("not a certificate")))
		Expect(err).To(MatchError(vault.ErrNotACertificate))
	})

	It("tells a certificate another key signed from a self-signed one", func() {
		public, _, _ := ed25519.GenerateKey(rand.Reader)
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, public, other)
		Expect(err).NotTo(HaveOccurred())
		certificate, err := vault.ParseCertificate(base64.StdEncoding.EncodeToString(der))
		Expect(err).NotTo(HaveOccurred())
		Expect(vault.SelfSigned(certificate)).To(BeFalse())
	})

	It("checks an ECDSA signature only under its own algorithm's name", func() {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
		Expect(err).NotTo(HaveOccurred())
		certificate, err := vault.ParseCertificate(base64.StdEncoding.EncodeToString(der))
		Expect(err).NotTo(HaveOccurred())
		digest := make([]byte, 32)
		signature, err := ecdsa.SignASN1(rand.Reader, private, digest)
		Expect(err).NotTo(HaveOccurred())
		Expect(vault.Verify(certificate, "ecdsa-sha256", digest, signature)).To(BeTrue())
		Expect(vault.Verify(certificate, "ed25519", digest, signature)).To(BeFalse())
	})
})
