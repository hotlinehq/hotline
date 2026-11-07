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
	borrowed, _ := p.pool.Get().(*pooledSigner)
	if borrowed == nil {
		borrowed = &pooledSigner{}
	}
	borrowed.s.mac = hmac.New(sha256.New, key)
	borrowed.s.n = 0
	return borrowed
}

// put takes a signer back, dropping its key and what its lists point to, so
// the pool keeps no call's key or strings alive.
func (p *signPool) put(borrowed *pooledSigner) {
	borrowed.s.mac = nil
	clear(borrowed.sorted)
	clear(borrowed.spans)
	borrowed.sorted, borrowed.spans = borrowed.sorted[:0], borrowed.spans[:0]
	p.pool.Put(borrowed)
}

// sum is the HMAC of everything written, in base64.
func (ps *pooledSigner) sum() string {
	ps.s.flush()
	return base64.StdEncoding.EncodeToString(ps.s.mac.Sum(ps.out[:0]))
}

// keyID names the key a record was signed with: the first 8 bytes of its
// SHA-256, in hex, so a reader can tell keys apart without seeing them.
func keyID(key []byte) attribute.KeyValue {
	sum := sha256.Sum256(key)
	return attribute.String("hotline.signature_key_id", hex.EncodeToString(sum[:8]))
}
