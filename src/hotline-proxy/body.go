package proxy

import (
	"errors"
	"io"
	"slices"
	"sync"

	"hotline-proxy/internal/ledger"
	"hotline-proxy/internal/spec"
)

// tap passes a body straight on, and hands each piece that passes to the
// parts that need it, each with one job (Peter, 2026-11-16, from the
// decomplect review): it counts the bytes, mac hashes every byte for the
// ledger, saver keeps a copy for the file exporters, and copy keeps a
// copy for the checks. done, if set, runs once when the body ends: read to
// the end, broken off, or closed early. The request body is read by the
// transport, which may still be reading when the record is made, so tap
// holds one lock for all its parts.
type tap struct {
	io.ReadCloser
	done     func()
	finished sync.Once
	// wait, if set, runs before each read and limits how long it may take.
	wait func() func() bool

	mac   *ledger.BodyHash
	saver bodySaver
	// copy keeps the body for the checks: whole, or a multipart request
	// body part by part. Never nil.
	copy bodyCopy
	// stream, when set, reads the body's stream items as they pass.
	stream *responseStream

	mu   sync.Mutex
	size int64
	err  error
}

func (t *tap) Read(p []byte) (int, error) {
	if t.wait != nil {
		defer t.wait()()
	}
	n, err := t.ReadCloser.Read(p)
	t.mu.Lock()
	t.size += int64(n)
	_, _ = t.mac.Write(p[:n])
	t.saver.add(p[:n])
	t.copy.add(p[:n])
	if t.stream != nil {
		t.stream.pass(p[:n])
		if errors.Is(err, io.EOF) {
			t.stream.endedWhole()
		}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.err = err
	}
	t.mu.Unlock()
	if err != nil {
		t.finish()
	}
	return n, err
}

func (t *tap) Close() error {
	t.finish()
	return t.ReadCloser.Close()
}

// finish runs done once. The body can end on two goroutines at once: a
// WebSocket's copy reads it to its end while the call ending closes it.
func (t *tap) finish() {
	t.finished.Do(func() {
		if t.done != nil {
			t.done()
		}
	})
}

// counts says how many bytes passed so far, and why the body broke off,
// if it did.
func (t *tap) counts() (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size, t.err
}

// hashed is the hash of the bytes that passed so far: of zero bytes when
// none did.
func (t *tap) hashed() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mac.Sum()
}

// savedBody is the part of the body saved for the file exporters, and whether
// the body was longer than that.
func (t *tap) savedBody() ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saver.data, t.size > int64(len(t.saver.data))
}

// copied is the copy of the body kept for the checks so far. The check
// reads it after the lock is let go, so the copy stays as it is until
// drop.
func (t *tap) copied() spec.Body {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.copy.hand()
}

// held is the memory the copy for the checks takes, or none before the
// first byte.
func (t *tap) held() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.copy.held()
}

// drop gives the copy's buffer back, once the check of the copy has ended.
func (t *tap) drop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.copy.drop()
}

// bodySaver keeps the first limit bytes of a body for the file exporters,
// under content: full; a limit of 0 keeps nothing. Unlike the copy for the
// checks, it stays until the record is made.
type bodySaver struct {
	limit int64
	// expect is the body's Content-Length, or -1 when it was not sent.
	expect int64
	data   []byte
}

func (s *bodySaver) add(b []byte) {
	room := min(int64(len(b)), s.limit-int64(len(s.data)))
	if room <= 0 {
		return
	}
	if s.data == nil && s.expect > 0 {
		// The whole body's room at once, when its length is known.
		s.data = make([]byte, 0, min(s.expect, s.limit))
	}
	s.data = append(s.data, b[:room]...)
}

// bodyCopy is how a body is kept for the checks as it passes: bodyKeeper
// keeps it whole, partReader keeps a multipart request body's text parts.
type bodyCopy interface {
	add(b []byte)
	// hand gives the copy to the check; it never changes after.
	hand() spec.Body
	// held is the memory the copy takes.
	held() int64
	// drop lets the copy go, once the check of it has ended.
	drop()
}

// formFiles are the file parts of a multipart request body read part by
// part, as far as it passed; nil for any other body.
func (t *tap) formFiles() []spec.FormPart {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.copy.(*partReader); ok {
		return slices.Clone(r.files)
	}
	return nil
}

// bodyKeeper keeps a copy of up to limit bytes of a body for the checks,
// in a buffer borrowed from pool; a limit of 0 keeps nothing.
type bodyKeeper struct {
	limit int
	pool  *bodyPool
	// expect is the body's Content-Length, or -1 when it was not sent.
	expect int64
	kept   []byte
	// buf is the borrowed buffer kept sits in, nil until the first byte.
	buf *bodyBuffer
	// over says the body was longer than limit.
	over bool
	// handed is set once the copy has gone to the check: from then on the
	// copy never changes, and its buffer is never swapped.
	handed bool
}

func (k *bodyKeeper) add(b []byte) {
	if k.limit <= 0 || k.handed {
		return
	}
	room := min(len(b), k.limit-len(k.kept))
	k.keep(b[:room])
	k.over = k.over || room < len(b)
}

// keep adds b to the copy. The first bytes borrow a buffer the size of the
// whole body, when its Content-Length says so; a copy that outgrows its
// buffer moves to a larger one and gives the old one back.
func (k *bodyKeeper) keep(b []byte) {
	if len(b) == 0 {
		return
	}
	need := len(k.kept) + len(b)
	if k.buf == nil || need > cap(k.buf.data) {
		want := int64(need)
		if k.buf == nil && k.expect > want {
			want = k.expect
		}
		buf := k.pool.get(int(min(want, int64(k.limit))))
		buf.data = append(buf.data, k.kept...)
		if k.buf != nil {
			k.pool.put(k.buf)
		}
		k.buf = buf
	}
	k.buf.data = append(k.buf.data, b...)
	k.kept = k.buf.data
}

// cut keeps only the first n bytes of the copy; what is kept next goes on
// from there.
func (k *bodyKeeper) cut(n int) {
	k.kept = k.kept[:n]
	if k.buf != nil {
		k.buf.data = k.kept
	}
}

func (k *bodyKeeper) hand() spec.Body {
	k.handed = true
	return spec.Body{Data: k.kept, TooLarge: k.over}
}

func (k *bodyKeeper) held() int64 {
	if k.buf == nil {
		return 0
	}
	return int64(cap(k.buf.data))
}

func (k *bodyKeeper) drop() {
	if k.buf != nil {
		k.pool.put(k.buf)
	}
	k.kept, k.buf, k.limit = nil, nil, 0
}

// bodyPool lends the buffers bodies are kept in, in eight sizes from 16 KB
// to 2 MiB, each twice the one before, so a buffer given back fits the
// next body of about its size (Peter, 2026-11-02, found by the load test).
type bodyPool struct{ sizes [8]sync.Pool }

// bodyBuffer is a borrowed buffer and the pool of its size.
type bodyBuffer struct {
	data []byte
	size int
}

// get lends an empty buffer of at least n bytes, up to the largest size.
func (p *bodyPool) get(n int) *bodyBuffer {
	i := 0
	for 16<<10<<i < n && i < len(p.sizes)-1 {
		i++
	}
	if b, ok := p.sizes[i].Get().(*bodyBuffer); ok {
		b.data = b.data[:0]
		return b
	}
	return &bodyBuffer{data: make([]byte, 0, 16<<10<<i), size: i}
}

// put gives a buffer back to the pool of its size.
func (p *bodyPool) put(b *bodyBuffer) { p.sizes[b.size].Put(b) }
