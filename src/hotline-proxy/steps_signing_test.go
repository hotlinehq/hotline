package proxy_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

// signingAuthority is a test certificate authority that signs the proxy's
// hourly keys: its certificates, its own first, then the ones above it, up
// to a root, and its key.
type signingAuthority struct {
	certs []*x509.Certificate
	key   crypto.Signer
}

// root is the top certificate: the one a person checking chooses to trust.
func (a *signingAuthority) root() *x509.Certificate { return a.certs[len(a.certs)-1] }

// testKey is a new key of a type a step names, with the name a batch
// record writes in front of its signatures and the options its Sign takes.
type testKey struct {
	signer    crypto.Signer
	algorithm string
	options   crypto.SignerOpts
}

func newTestKey(kind string) testKey {
	switch kind {
	case "ECDSA P-256", "ecdsa-sha256":
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		return testKey{key, "ecdsa-sha256", crypto.SHA256}
	case "RSA 2048", "rsa-pss-sha256":
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		return testKey{key, "rsa-pss-sha256", &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}}
	case "rsa-pkcs1-sha256":
		key, _ := rsa.GenerateKey(rand.Reader, 2048)
		return testKey{key, "rsa-pkcs1-sha256", crypto.SHA256}
	case "ML-DSA-44", "ml-dsa-44", "ML-DSA-65", "ml-dsa-65", "ML-DSA-87", "ml-dsa-87":
		parameters := map[string]mldsa.Parameters{"44": mldsa.MLDSA44(), "65": mldsa.MLDSA65(), "87": mldsa.MLDSA87()}[kind[len(kind)-2:]]
		key, _ := mldsa.GenerateKey(parameters)
		return testKey{key, "ml-dsa-" + kind[len(kind)-2:], &mldsa.Options{}}
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	return testKey{key, "ed25519", crypto.Hash(0)}
}

// sign is the signature of digest, as a batch record writes it.
func (k testKey) sign(digest []byte) string {
	signature, err := k.signer.Sign(rand.Reader, digest, k.options)
	if err != nil {
		panic(err)
	}
	return k.algorithm + ":" + base64.StdEncoding.EncodeToString(signature)
}

// testCert is a certificate for key, naming name, valid from from to to,
// an authority or not, signed by the parent's key, or by key itself when
// parent is nil.
func testCert(name string, key crypto.Signer, from, to time.Time, authority bool, parent *x509.Certificate, parentKey crypto.Signer) *x509.Certificate {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: from, NotAfter: to,
		IsCA: authority, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if authority {
		template.KeyUsage = x509.KeyUsageCertSign
	}
	if parent == nil {
		parent, parentKey = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), parentKey)
	if err != nil {
		panic(err)
	}
	certificate, _ := x509.ParseCertificate(der)
	return certificate
}

// newSigningAuthority is an authority with a key of the kind, valid from
// an hour ago until until, signed by the one above it or by itself.
func newSigningAuthority(kind, name string, above *signingAuthority, until time.Time, authority bool) *signingAuthority {
	key := newTestKey(kind).signer
	if above == nil {
		return &signingAuthority{certs: []*x509.Certificate{testCert(name, key, time.Now().Add(-time.Hour), until, authority, nil, nil)}, key: key}
	}
	own := testCert(name, key, time.Now().Add(-time.Hour), until, authority, above.certs[0], above.key)
	return &signingAuthority{certs: append([]*x509.Certificate{own}, above.certs...), key: key}
}

// useSigningAuthority writes the authority's files and names it batches in
// certs, and writes its root to root.pem, for verify's --root-authority.
func (w *world) useSigningAuthority(a *signingAuthority) {
	var certs strings.Builder
	for _, c := range a.certs {
		certs.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	key, _ := x509.MarshalPKCS8PrivateKey(a.key)
	w.writeFile("batches/cert.pem", certs.String())
	w.writeFile("batches/key.pem", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})))
	w.writeFile("root.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.root().Raw})))
	if w.certEntries == "" {
		w.certEntries = "certs:\n"
	}
	if !strings.Contains(w.certEntries, "  batches:") {
		w.certEntries += "  batches: { cert: ./batches/cert.pem, key: ./batches/key.pem }\n"
	}
}

// authoritiesOf are the certificates a batch record's
// hotline.batch.authorities holds, in order.
func authoritiesOf(r receivedRecord) ([]*x509.Certificate, error) {
	list, _ := r.attrs["hotline.batch.authorities"].([]any)
	var out []*x509.Certificate
	for _, item := range list {
		der, err := base64.StdEncoding.DecodeString(sprint(item))
		if err != nil {
			return nil, err
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// checkSignature tells whether signature, as a batch record writes it, is
// the signature of digest by the certificate's key, checked with the
// standard library on its own.
func checkSignature(certificate *x509.Certificate, written string, digest []byte) error {
	algorithm, encoded, _ := strings.Cut(written, ":")
	signature, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	ok := false
	switch key := certificate.PublicKey.(type) {
	case ed25519.PublicKey:
		ok = algorithm == "ed25519" && ed25519.Verify(key, digest, signature)
	case *ecdsa.PublicKey:
		ok = algorithm == "ecdsa-sha256" && ecdsa.VerifyASN1(key, digest, signature)
	case *rsa.PublicKey:
		ok = algorithm == "rsa-pss-sha256" && rsa.VerifyPSS(key, crypto.SHA256, digest, signature, nil) == nil ||
			algorithm == "rsa-pkcs1-sha256" && rsa.VerifyPKCS1v15(key, crypto.SHA256, digest, signature) == nil
	case *mldsa.PublicKey:
		ok = strings.EqualFold(algorithm, key.Parameters().String()) && mldsa.Verify(key, digest, signature, &mldsa.Options{}) == nil
	}
	if !ok {
		return failf("the hotline.batch.signature %s… does not check out with its key", written[:min(len(written), 24)])
	}
	return nil
}

// keyKind is the name of a certificate's key type, as a step writes it.
func keyKind(certificate *x509.Certificate) string {
	switch key := certificate.PublicKey.(type) {
	case ed25519.PublicKey:
		return "Ed25519"
	case *mldsa.PublicKey:
		return key.Parameters().String()
	}
	return fmt.Sprintf("%T", certificate.PublicKey)
}

// signingSteps are the steps of signing the batches with the proxy's own
// keys, by themselves or under an authority from certs, and of verify's
// checks of them.
func signingSteps(sc *godog.ScenarioContext, w *world, result *verified) {
	// authority is the scenario's authority named batches in certs, and
	// first is the one before a reload replaced it.
	var authority, first *signingAuthority
	firstBatch := func() (receivedRecord, *x509.Certificate, error) {
		batches, err := w.waitBatches(1)
		if err != nil {
			return receivedRecord{}, nil, err
		}
		certificate, err := certificateOf(batches[0])
		return batches[0], certificate, err
	}
	sc.Step(`^the batch record's certificate is not a certificate authority$`, func() error {
		_, certificate, err := firstBatch()
		if err != nil {
			return err
		}
		if certificate.IsCA || !certificate.BasicConstraintsValid {
			return failf("the certificate says IsCA %v, with basic constraints %v", certificate.IsCA, certificate.BasicConstraintsValid)
		}
		return nil
	})
	sc.Step(`^the batch record has no hotline.batch.authorities$`, func() error {
		batch, _, err := firstBatch()
		if err != nil {
			return err
		}
		if authorities, found := batch.attrs["hotline.batch.authorities"]; found {
			return failf("the batch record has hotline.batch.authorities %v", authorities)
		}
		return nil
	})
	sc.Step(`^the config sets ledger signing hourlyKeyType (\S+)$`, func(keyType string) error {
		w.signing += "    hourlyKeyType: " + keyType + "\n"
		return nil
	})
	sc.Step(`^the batch record's hotline.batch.certificate is for an? (\S+) key, signed by that key$`, func(kind string) error {
		_, certificate, err := firstBatch()
		if err != nil {
			return err
		}
		if keyKind(certificate) != kind {
			return failf("the certificate's key is %s", keyKind(certificate))
		}
		return certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature)
	})
	sc.Step(`^the batch record's hotline.batch.signature starts with (\S+)$`, func(prefix string) error {
		batch, _, err := firstBatch()
		if err != nil {
			return err
		}
		if signature := sprint(batch.attrs["hotline.batch.signature"]); !strings.HasPrefix(signature, prefix) {
			return failf("the signature is %s", signature)
		}
		return nil
	})
	sc.Step(`^an? (Ed25519|ECDSA P-256|RSA 2048|ML-DSA-65) certificate authority named batches in certs(, below a root authority)?$`, func(kind, below string) error {
		var root *signingAuthority
		if below != "" {
			root = newSigningAuthority("Ed25519", "hotline test root authority", nil, time.Now().Add(48*time.Hour), true)
		}
		authority = newSigningAuthority(kind, "hotline test batch authority", root, time.Now().Add(24*time.Hour), true)
		w.useSigningAuthority(authority)
		return nil
	})
	sc.Step(`^a certificate authority named batches in certs that expires in half an hour$`, func() error {
		authority = newSigningAuthority("Ed25519", "hotline test batch authority", nil, w.clock.Now().Add(30*time.Minute), true)
		w.useSigningAuthority(authority)
		return nil
	})
	sc.Step(`^a certificate named batches in certs that is not a certificate authority$`, func() error {
		authority = newSigningAuthority("Ed25519", "hotline test batch authority", nil, time.Now().Add(24*time.Hour), false)
		w.useSigningAuthority(authority)
		return nil
	})
	sc.Step(`^a certificate authority named batches in certs, followed by a root that did not sign it$`, func() error {
		authority = newSigningAuthority("Ed25519", "hotline test batch authority", nil, time.Now().Add(24*time.Hour), true)
		stranger := newSigningAuthority("Ed25519", "hotline test stranger", nil, time.Now().Add(24*time.Hour), true)
		authority.certs = append(authority.certs, stranger.certs...)
		w.useSigningAuthority(authority)
		return nil
	})
	sc.Step(`^the config signs the hourly keys with the cert (\S+)$`, func(name string) error {
		w.signing += "    cert: " + name + "\n"
		return nil
	})
	sc.Step(`^the batch record's certificate is signed by the batches authority$`, func() error {
		_, certificate, err := firstBatch()
		if err != nil {
			return err
		}
		return certificate.CheckSignatureFrom(authority.certs[0])
	})
	sc.Step(`^hotline.batch.authorities holds the batches authority's certificate(, then the root's)?$`, func(root string) error {
		batch, _, err := firstBatch()
		if err != nil {
			return err
		}
		got, err := authoritiesOf(batch)
		if err != nil {
			return err
		}
		want := 1
		if root != "" {
			want = 2
		}
		if len(got) != want || len(authority.certs) != want {
			return failf("hotline.batch.authorities holds %d certificates, want %d", len(got), want)
		}
		for i := range got {
			if !got[i].Equal(authority.certs[i]) {
				return failf("certificate %d of hotline.batch.authorities is %s", i+1, got[i].Subject.CommonName)
			}
		}
		return nil
	})
	sc.Step(`^the batches cert is replaced with (another certificate authority|a certificate that is not a certificate authority) and the config reloaded$`, func(with string) error {
		first = authority
		authority = newSigningAuthority("Ed25519", "hotline test new batch authority", nil, time.Now().Add(24*time.Hour), with == "another certificate authority")
		w.useSigningAuthority(authority)
		return w.reloadConfig()
	})
	sc.Step(`^the next batch's certificate is (new and signed by the new authority|the same, signed by the first authority)$`, func(which string) error {
		batches, err := w.waitBatches(2)
		if err != nil {
			return err
		}
		last := batches[len(batches)-1]
		before, _ := certificateOf(batches[0])
		certificate, err := certificateOf(last)
		if err != nil {
			return err
		}
		signer := authority
		if strings.HasPrefix(which, "the same") {
			signer = first
			if !certificate.Equal(before) {
				return failf("the next batch has a new certificate, though the reload was refused")
			}
		} else if certificate.Equal(before) {
			return failf("the next batch kept the certificate of the batch before the reload")
		}
		return certificate.CheckSignatureFrom(signer.certs[0])
	})
	sc.Step(`^the signing config is set to the cert batches and reloaded$`, func() error {
		w.signing += "    cert: batches\n"
		return w.reloadConfig()
	})
	sc.Step(`^hotline.certificate.not_after is the batches authority's expiry for hotline.certificate.use ledger.signing$`, func() error {
		return w.ownValue("hotline.certificate.not_after", "hotline.certificate.use ledger.signing", float64(authority.certs[0].NotAfter.Unix()))
	})

	// Batch records made by hand, as someone else's signer or a forger
	// would write them, for what the proxy itself never makes.
	sc.Step(`^the first batch record is signed again by an? (\S+) key, its certificate signed by itself$`, func(algorithm string) error {
		return w.resignBatch0(newTestKey(algorithm), nil, false, nil)
	})
	sc.Step(`^the first batch record is signed again by a key whose certificate (claims to be a certificate authority|an authority signed, with the authorities swapped for another authority's)$`, func(how string) error {
		if strings.HasPrefix(how, "claims") {
			return w.resignBatch0(newTestKey("ed25519"), nil, true, nil)
		}
		signer := newSigningAuthority("Ed25519", "hotline test batch authority", nil, time.Now().Add(24*time.Hour), true)
		stranger := newSigningAuthority("Ed25519", "hotline test stranger", nil, time.Now().Add(24*time.Hour), true)
		return w.resignBatch0(newTestKey("ed25519"), signer, false, stranger.certs)
	})
	sc.Step(`^one byte of the first batch record's signature is changed$`, func() error {
		return w.changeBatchLine(0, func(line map[string]any) {
			attrs := line["attributes"].(map[string]any)
			algorithm, encoded, _ := strings.Cut(sprint(attrs["hotline.batch.signature"]), ":")
			signature, _ := base64.StdEncoding.DecodeString(encoded)
			signature[0] ^= 1
			attrs["hotline.batch.signature"] = algorithm + ":" + base64.StdEncoding.EncodeToString(signature)
		})
	})
	sc.Step(`^the run's and the summary's batch_signing is (\S+)(?: and root_authorities names "([^"]*)")?$`, func(signing, root string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		summary, _ := report["summary"].(map[string]any)
		runs, _ := report["runs"].([]any)
		if len(runs) == 0 {
			return failf("the report has no run: %s", result.out)
		}
		for _, part := range append([]any{summary}, runs...) {
			fields, _ := part.(map[string]any)
			if fields["batch_signing"] != signing {
				return failf("batch_signing is %v, want %s: %s", fields["batch_signing"], signing, result.out)
			}
			roots, _ := fields["root_authorities"].([]any)
			if root == "" && len(roots) != 0 || root != "" && (len(roots) != 1 || roots[0] != root) {
				return failf("root_authorities is %v, want %q: %s", roots, root, result.out)
			}
		}
		return nil
	})
	sc.Step(`^the report has no note key_self_certified$`, func() error {
		report, err := result.json()
		if err != nil {
			return err
		}
		notes, _ := report["notes"].([]any)
		for _, n := range notes {
			if note, _ := n.(map[string]any); note["code"] == "key_self_certified" {
				return failf("the report has the note key_self_certified: %s", result.out)
			}
		}
		return nil
	})
	sc.Step(`^the problems hold one with code (\S+) naming "([^"]*)"$`, func(code, words string) error {
		report, err := result.json()
		if err != nil {
			return err
		}
		problems, _ := report["problems"].([]any)
		for _, p := range problems {
			problem, _ := p.(map[string]any)
			if problem["code"] == code && strings.Contains(sprint(problem["message"]), words) {
				return nil
			}
		}
		return failf("no problem with code %s naming %q: %s", code, words, result.out)
	})
}

// resignBatch0 gives the first batch record a new certificate for key and
// signs it again with key. The certificate names the service, covers the
// batch's close time, and is signed by key itself or by the authority;
// authorities, when given, take the place of the authority's own.
func (w *world) resignBatch0(key testKey, by *signingAuthority, claimsAuthority bool, authorities []*x509.Certificate) error {
	return w.changeBatchLine(0, func(line map[string]any) {
		attrs := line["attributes"].(map[string]any)
		at, _ := time.Parse(time.RFC3339Nano, sprint(line["time"]))
		from, to := at.Add(-time.Minute), at.Add(time.Hour)
		var certificate *x509.Certificate
		if by == nil {
			certificate = testCert("hotline-proxy", key.signer, from, to, claimsAuthority, nil, nil)
		} else {
			certificate = testCert("hotline-proxy", key.signer, from, to, claimsAuthority, by.certs[0], by.key)
			if authorities == nil {
				authorities = by.certs
			}
		}
		attrs["hotline.batch.certificate"] = base64.StdEncoding.EncodeToString(certificate.Raw)
		delete(attrs, "hotline.batch.authorities")
		if len(authorities) > 0 {
			var list []any
			for _, c := range authorities {
				list = append(list, base64.StdEncoding.EncodeToString(c.Raw))
			}
			attrs["hotline.batch.authorities"] = list
		}
		digest := signedDigest(lineAsReceived(line))
		attrs["hotline.batch.signature"] = key.sign(digest[:])
	})
}
