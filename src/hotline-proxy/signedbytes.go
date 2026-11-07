package proxy

import (
	"encoding/binary"
	"encoding/hex"
	"hash"
	"math"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// signer feeds the bytes a signature covers straight into the HMAC, through
// a small buffer, so a signature makes no text of its own (Peter,
// 2026-11-02). Each piece is its length as 8 bytes, then its bytes; each
// entry is its name as a piece, then its value. Nothing is quoted or
// escaped: the lengths say where each piece ends.
type signer struct {
	mac hash.Hash
	buf [512]byte
	n   int
}

// write adds bytes, sending the buffer on to the HMAC when it is full.
func (s *signer) write(p []byte) {
	for len(p) > 0 {
		if s.n == len(s.buf) {
			s.flush()
		}
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
	}
}

func (s *signer) flush() {
	s.mac.Write(s.buf[:s.n])
	s.n = 0
}

// writeString adds text, as write does.
func (s *signer) writeString(p string) {
	for len(p) > 0 {
		if s.n == len(s.buf) {
			s.flush()
		}
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
	}
}

func (s *signer) byte(b byte) { s.write([]byte{b}) }

func (s *signer) uint(u uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], u)
	s.write(b[:])
}

func (s *signer) piece(p string) {
	s.uint(uint64(len(p)))
	s.writeString(p)
}

// text is an entry with a text value.
func (s *signer) text(name, value string) {
	s.piece(name)
	s.byte('s')
	s.piece(value)
}

// int is an entry with a whole number.
func (s *signer) int(name string, value int64) {
	s.piece(name)
	s.byte('i')
	s.uint(uint64(value)) //nolint:gosec // the bits as they are
}

// hex is an entry with an id as hex text.
func (s *signer) hex(name string, id []byte) {
	var b [32]byte
	text := b[:hex.Encode(b[:], id)]
	s.piece(name)
	s.byte('s')
	s.uint(uint64(len(text)))
	s.write(text)
}

// entry is an attribute: its name, then its value.
func (s *signer) entry(kv attribute.KeyValue) {
	s.piece(string(kv.Key))
	s.value(kv.Value)
}

// value is a type letter, then: text as a piece; a whole or decimal number
// as 8 bytes; true or false as one byte; a list or map as its count in 8
// bytes, then each item, a map's in name order.
func (s *signer) value(v attribute.Value) {
	switch v.Type() {
	case attribute.STRING:
		s.byte('s')
		s.piece(v.AsString())
	case attribute.INT64:
		s.byte('i')
		s.uint(uint64(v.AsInt64())) //nolint:gosec // the bits as they are
	case attribute.FLOAT64:
		s.byte('f')
		s.uint(math.Float64bits(v.AsFloat64()))
	case attribute.BOOL:
		s.byte('b')
		if v.AsBool() {
			s.byte(1)
		} else {
			s.byte(0)
		}
	case attribute.STRINGSLICE:
		items := v.AsStringSlice()
		s.list(uint64(len(items)))
		for _, item := range items {
			s.value(attribute.StringValue(item))
		}
	case attribute.SLICE:
		items := v.AsSlice()
		s.list(uint64(len(items)))
		for _, item := range items {
			s.value(item)
		}
	default:
		// A map, as in a list of check results: the only other kind a
		// record holds.
		entries := slices.SortedFunc(slices.Values(v.AsMap()), byKey)
		s.byte('m')
		s.uint(uint64(len(entries)))
		for _, kv := range entries {
			s.entry(kv)
		}
	}
}

func (s *signer) list(n uint64) {
	s.byte('l')
	s.uint(n)
}

func byKey(a, b attribute.KeyValue) int { return strings.Compare(string(a.Key), string(b.Key)) }
