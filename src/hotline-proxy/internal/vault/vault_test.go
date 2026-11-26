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
	It("never prints its private part", func() {
		key := vault.NewKey("hotline-proxy", time.Now(), time.Hour)
		for _, text := range []string{fmt.Sprint(key), fmt.Sprintf("%v %+v %#v %s", key, key, key, key)} {
			Expect(text).NotTo(ContainSubstring(key.Certificate))
			Expect(text).To(ContainSubstring("redacted"))
		}
	})

	start := time.UnixMicro(1_700_000_000_000_000)

	It("gives the same key until it is an hour old, then a new one", func() {
		v := vault.New("hotline-proxy", time.Hour, start)
		first := v.Key(start)
		Expect(v.Key(start.Add(59 * time.Minute)).Certificate).To(Equal(first.Certificate))
		Expect(v.Key(start.Add(time.Hour)).Certificate).NotTo(Equal(first.Certificate))
	})
})

var _ = Describe("Reading a batch certificate", func() {
	It("names the service and covers the key's whole hour, in whole seconds", func() {
		made := time.Date(2026, 11, 26, 8, 0, 0, 500_000_000, time.UTC)
		certificate, err := vault.ParseCertificate(vault.NewKey("hotline-proxy", made, time.Hour).Certificate)
		Expect(err).NotTo(HaveOccurred())
		Expect(certificate.Subject.CommonName).To(Equal("hotline-proxy"))
		Expect(certificate.NotBefore).To(Equal(made.Truncate(time.Second)))
		Expect(certificate.NotAfter).To(Equal(made.Add(time.Hour).Truncate(time.Second).Add(time.Second)))
	})

	It("refuses text that is not base64", func() {
		_, err := vault.ParseCertificate("!!")
		Expect(err).To(MatchError(vault.ErrNotACertificate))
	})

	It("refuses a certificate for a key that is not Ed25519", func() {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
		Expect(err).NotTo(HaveOccurred())
		_, err = vault.ParseCertificate(base64.StdEncoding.EncodeToString(der))
		Expect(err).To(MatchError(vault.ErrNotACertificate))
	})

	It("refuses a certificate another key signed", func() {
		public, _, _ := ed25519.GenerateKey(rand.Reader)
		_, other, _ := ed25519.GenerateKey(rand.Reader)
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, template, template, public, other)
		Expect(err).NotTo(HaveOccurred())
		_, err = vault.ParseCertificate(base64.StdEncoding.EncodeToString(der))
		Expect(err).To(MatchError(vault.ErrNotSelfSigned))
	})
})
