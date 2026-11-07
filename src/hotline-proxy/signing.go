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

// signPool hands out signers for one signing key. A signer keeps its keyed
// hash and its lists between calls, so signing a record or a trace no longer
// sets up an HMAC or allocates its lists every time. Only memory is reused:
// the signed bytes are the same as from a signer that is new, so anyone with
// the key and the record can still rebuild the signature.
type signPool struct {
	keyID attribute.KeyValue
	pool  sync.Pool
}

// pooledSigner is a signer with the lists it sorts in, kept for reuse.
type pooledSigner struct {
	s      signer
	sorted []attribute.KeyValue
	spans  []export.Span
	out    [sha256.Size]byte
}

func newSignPool(key []byte) *signPool {
	sum := sha256.Sum256(key)
	p := &signPool{keyID: attribute.String("hotline.signature_key_id", hex.EncodeToString(sum[:8]))}
	// The key is copied, so a later change to the caller's bytes cannot
	// change what this pool signs with.
	own := append([]byte(nil), key...)
	p.pool.New = func() any { return &pooledSigner{s: signer{mac: hmac.New(sha256.New, own)}} }
	return p
}

// get gives a signer with nothing written yet.
func (p *signPool) get() *pooledSigner {
	x, _ := p.pool.Get().(*pooledSigner)
	x.s.mac.Reset()
	x.s.n = 0
	return x
}

// put takes a signer back, dropping what its lists point to so the pool
// keeps no call's strings alive.
func (p *signPool) put(x *pooledSigner) {
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

// signers are the signing pools of the keys in use, one per key, so two keys
// never share a signer. A key that is reloaded away keeps its small pool.
type signers struct {
	mu    sync.RWMutex
	byKey map[string]*signPool
}

// forKey is the pool of key, made on first use.
func (s *signers) forKey(key []byte) *signPool {
	s.mu.RLock()
	p := s.byKey[string(key)]
	s.mu.RUnlock()
	if p != nil {
		return p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p = s.byKey[string(key)]; p == nil {
		if s.byKey == nil {
			s.byKey = map[string]*signPool{}
		}
		p = newSignPool(key)
		s.byKey[string(key)] = p
	}
	return p
}
