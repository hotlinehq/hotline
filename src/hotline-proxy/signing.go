package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"hotline-proxy/internal/export"
)

// signPool hands out signers to every integration's calls. A signer keeps
// its buffer and the lists it sorts in between calls, so signing a record or
// a trace no longer allocates them every time. The keyed hash is set up for
// each signature with that call's key, so one pool serves all integrations
// and no key stays in a reused signer. Only memory is reused: the signed
// bytes are the same as from a signer that is new, so anyone with the key
// and the record can still rebuild the signature.
type signPool struct {
	pool sync.Pool
}

// pooledSigner is a signer with the lists it sorts in, kept for reuse.
type pooledSigner struct {
	s      signer
	sorted []attribute.KeyValue
	spans  []export.Span
	out    [sha256.Size]byte
}

// get gives a signer with nothing written yet, keyed with key.
func (p *signPool) get(key []byte) *pooledSigner {
	x, _ := p.pool.Get().(*pooledSigner)
	if x == nil {
		x = &pooledSigner{}
	}
	x.s.mac = hmac.New(sha256.New, key)
	x.s.n = 0
	return x
}

// put takes a signer back, dropping its key and what its lists point to, so
// the pool keeps no call's key or strings alive.
func (p *signPool) put(x *pooledSigner) {
	x.s.mac = nil
	clear(x.sorted)
	clear(x.spans)
	x.sorted, x.spans = x.sorted[:0], x.spans[:0]
	p.pool.Put(x)
}

// sum is the HMAC of everything written, in base64.
func (x *pooledSigner) sum() string {
	x.s.flush()
	return base64.StdEncoding.EncodeToString(x.s.mac.Sum(x.out[:0]))
}

// keyID names the key a record was signed with: the first 8 bytes of its
// SHA-256, in hex, so a reader can tell keys apart without seeing them.
func keyID(key []byte) attribute.KeyValue {
	sum := sha256.Sum256(key)
	return attribute.String("hotline.signature_key_id", hex.EncodeToString(sum[:8]))
}
