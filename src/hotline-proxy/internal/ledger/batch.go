package ledger

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/vault"
)

// ErrSignatureInvalid is a batch record whose signature does not check
// out with its certificate's key.
var ErrSignatureInvalid = errors.New("ledger: the signature does not check out with the certificate's key")

const signaturePrefix = "ed25519:"

// Root is the root of a batch's tree, built as in Certificate Transparency
// (RFC 6962): a record's leaf is SHA-256 of 0x00 and its hash, an inner
// node SHA-256 of 0x01, the left and the right. The left part holds the
// largest power of two below the count. An empty batch's root is SHA-256 of
// nothing.
func Root(leaves [][sha256.Size]byte) [sha256.Size]byte {
	if len(leaves) == 0 {
		return sha256.Sum256(nil)
	}
	if len(leaves) == 1 {
		var b [1 + sha256.Size]byte
		copy(b[1:], leaves[0][:])
		return sha256.Sum256(b[:])
	}
	split := 1
	for split*2 < len(leaves) {
		split *= 2
	}
	left, right := Root(leaves[:split]), Root(leaves[split:])
	var b [1 + 2*sha256.Size]byte
	b[0] = 1
	copy(b[1:], left[:])
	copy(b[1+sha256.Size:], right[:])
	return sha256.Sum256(b[:])
}

// The attributes that hold a signature and the certificate whose key
// checks it.
const (
	batchSignature       = "hotline.batch.signature"
	certificateAttribute = "hotline.batch.certificate"
)

// signedDigest is what a batch record's signature covers: SHA-256 of its
// format, every attribute but its own signature sorted by name, then the
// record's other fields, as a record's hash covers them. Every other
// attribute is signed, so nothing can be added to a signed record.
func signedDigest(format string, r Record) [sha256.Size]byte {
	signed := make([]attribute.KeyValue, 0, len(r.Attrs))
	for _, kv := range r.Attrs {
		if string(kv.Key) != batchSignature {
			signed = append(signed, kv)
		}
	}
	w := newWriter()
	w.piece(format)
	w.entries(signed)
	w.fields(r)
	return w.sum()
}

// Sign signs a batch record of format with key, and gives its attributes
// with the certificate and the signature added: hotline.batch.certificate,
// then hotline.batch.signature, "ed25519:" and the signature in base64.
func Sign(format string, r Record, key vault.Key) []attribute.KeyValue {
	r.Attrs = append(slices.Clip(r.Attrs), attribute.String(certificateAttribute, key.Certificate))
	digest := signedDigest(format, r)
	signature := key.Sign(digest[:])
	return append(r.Attrs, attribute.String(batchSignature, signaturePrefix+base64.StdEncoding.EncodeToString(signature)))
}

// CheckSignature checks a batch record of format: its certificate, read
// as vault.ParseCertificate reads it, and its signature with the
// certificate's key. It gives the certificate, and an error that says
// which check failed: vault.ErrNotACertificate, vault.ErrNotSelfSigned or
// ErrSignatureInvalid.
func CheckSignature(format string, r Record) (*x509.Certificate, error) {
	var text, signature string
	for _, kv := range r.Attrs {
		switch string(kv.Key) {
		case certificateAttribute:
			text = kv.Value.AsString()
		case batchSignature:
			signature = kv.Value.AsString()
		}
	}
	certificate, err := vault.ParseCertificate(text)
	if err != nil {
		return nil, err
	}
	body, found := strings.CutPrefix(signature, signaturePrefix)
	sig, err := base64.StdEncoding.DecodeString(body)
	digest := signedDigest(format, r)
	if !found || err != nil || !vault.Verify(certificate, digest[:], sig) {
		return certificate, ErrSignatureInvalid
	}
	return certificate, nil
}

// NewBatchID is a UUID version 7: the time in milliseconds, then random
// bits, so batch ids sorted as text are in time order. Two ids made in the
// same millisecond are kept in order by counting up from the previous id.
func NewBatchID(at time.Time, previous string) string {
	var id [16]byte
	_, _ = rand.Read(id[6:])
	ms := uint64(at.UnixMilli())
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], ms)
	copy(id[:6], stamp[2:])
	id[6] = 0x70 | id[6]&0x0f
	id[8] = 0x80 | id[8]&0x3f
	text := format(id)
	if previous != "" && text <= previous {
		// The clock did not move on; follow the previous id instead.
		text = next(previous)
	}
	return text
}

func format(id [16]byte) string {
	h := hex.EncodeToString(id[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// next is the id one above previous, in its random bits.
func next(previous string) string {
	// previous is always an id this package wrote, so it reads back whole.
	var id [16]byte
	raw, _ := hex.DecodeString(strings.ReplaceAll(previous, "-", ""))
	copy(id[:], raw)
	for i := 15; i >= 9; i-- {
		id[i]++
		if id[i] != 0 {
			break
		}
	}
	return format(id)
}
