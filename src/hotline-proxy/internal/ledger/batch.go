package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// ErrNotAKey is a text that is not a public key as the ledger writes it.
var ErrNotAKey = errors.New("ledger: not an ed25519 key")

const (
	keyPrefix       = "ed25519:"
	signaturePrefix = "ed25519:"
)

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

// The attributes that hold a signature and the key that checks it.
const (
	batchSignature   = "hotline.batch.signature"
	droppedSignature = "hotline.dropped.signature"
	keyAttribute     = "hotline.batch.key"
)

// signatureOf is the attribute that holds the signature of a record of
// format.
func signatureOf(format string) string {
	if format == DroppedFormat {
		return droppedSignature
	}
	return batchSignature
}

// signedDigest is what a batch or drop record's signature covers: SHA-256
// of its format, every attribute but its own signature sorted by name,
// then the record's other fields, as a record's hash covers them. Every
// other attribute is signed, so nothing can be added to a signed record.
func signedDigest(format string, r Record) [sha256.Size]byte {
	unsigned := signatureOf(format)
	signed := make([]attribute.KeyValue, 0, len(r.Attrs))
	for _, kv := range r.Attrs {
		if string(kv.Key) != unsigned {
			signed = append(signed, kv)
		}
	}
	w := newWriter()
	w.piece(format)
	w.entries(signed)
	w.fields(r)
	return w.sum()
}

// Key is a signing key the proxy makes in memory and never writes down.
// It prints as redacted, so a log line can never show it.
type Key struct {
	private ed25519.PrivateKey
	// Public is the public key as the batch record holds it.
	Public string
}

// NewKey makes a new key from the system's random source.
func NewKey() Key {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	return Key{private: private, Public: keyPrefix + base64.StdEncoding.EncodeToString(public)}
}

// String hides the key when it is printed.
func (Key) String() string { return "redacted" }

// GoString hides the key when it is printed with %#v.
func (Key) GoString() string { return "redacted" }

// Sign signs a batch or drop record of format, and gives its attributes
// with the key and the signature added: hotline.batch.key, then
// hotline.batch.signature or hotline.dropped.signature.
func (k Key) Sign(format string, r Record) []attribute.KeyValue {
	r.Attrs = append(slices.Clip(r.Attrs), attribute.String(keyAttribute, k.Public))
	digest := signedDigest(format, r)
	signature := ed25519.Sign(k.private, digest[:])
	return append(r.Attrs, attribute.String(signatureOf(format), signaturePrefix+base64.StdEncoding.EncodeToString(signature)))
}

// CheckSignature says whether a batch or drop record of format checks out
// with the key it names.
func CheckSignature(format string, r Record) bool {
	var key, signature string
	for _, kv := range r.Attrs {
		switch string(kv.Key) {
		case keyAttribute:
			key = kv.Value.AsString()
		case signatureOf(format):
			signature = kv.Value.AsString()
		}
	}
	public, err := decode(key, keyPrefix)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return false
	}
	sig, err := decode(signature, signaturePrefix)
	if err != nil {
		return false
	}
	digest := signedDigest(format, r)
	return ed25519.Verify(public, digest[:], sig)
}

func decode(text, prefix string) ([]byte, error) {
	body, found := strings.CutPrefix(text, prefix)
	if !found {
		return nil, ErrNotAKey
	}
	return base64.StdEncoding.DecodeString(body)
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
