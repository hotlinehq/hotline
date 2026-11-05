package katydidcheck

import (
	"bytes"
	"errors"
	"strings"
	"unicode/utf8"
)

// Well-formedness rules for characters and names, from the XML 1.0 (fifth edition) rules.

var (
	errChar    = errors.New("xml: character not allowed in XML")
	errName    = errors.New("xml: bad name")
	errRef     = errors.New("xml: bad entity or character reference")
	errComment = errors.New("xml: bad comment")
	errPI      = errors.New("xml: bad processing instruction")
	errDecl    = errors.New("xml: bad xml declaration")
	errAttr    = errors.New("xml: bad attribute")
	errCDEnd   = errors.New("xml: ]]> is not allowed in text")
)

func isNameStart(r rune) bool {
	switch {
	case r == ':' || r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
		return true
	case r >= 0xC0 && r <= 0xD6, r >= 0xD8 && r <= 0xF6, r >= 0xF8 && r <= 0x2FF, r >= 0x370 && r <= 0x37D,
		r >= 0x37F && r <= 0x1FFF, r >= 0x200C && r <= 0x200D, r >= 0x2070 && r <= 0x218F,
		r >= 0x2C00 && r <= 0x2FEF, r >= 0x3001 && r <= 0xD7FF, r >= 0xF900 && r <= 0xFDCF,
		r >= 0xFDF0 && r <= 0xFFFD, r >= 0x10000 && r <= 0xEFFFF:
		return true
	}
	return false
}

func isNameChar(r rune) bool {
	return isNameStart(r) || r == '-' || r == '.' || (r >= '0' && r <= '9') || r == 0xB7 ||
		(r >= 0x300 && r <= 0x36F) || (r >= 0x203F && r <= 0x2040)
}

func validName(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for i := 0; i < len(b); {
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n <= 1 {
			return false
		}
		if i == 0 && !isNameStart(r) || i > 0 && !isNameChar(r) {
			return false
		}
		i += n
	}
	return true
}

func isXMLChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}

// checkChars refuses bytes that are not valid UTF-8 or not allowed characters. ASCII text takes a fast path.
func checkChars(b []byte) error {
	for i := 0; i < len(b); {
		c := b[i]
		if c >= 0x20 && c < 0x80 || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c < 0x20 {
			return errChar
		}
		r, n := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && n <= 1 || !isXMLChar(r) {
			return errChar
		}
		i += n
	}
	return nil
}

// checkRefs refuses a bare & and any reference except the five predefined ones and valid numeric ones.
func checkRefs(b []byte) error {
	for i := 0; i < len(b); i++ {
		if b[i] != '&' {
			continue
		}
		e := bytes.IndexByte(b[i:], ';')
		if e < 0 {
			return errRef
		}
		ent := b[i+1 : i+e]
		switch string(ent) {
		case "lt", "gt", "amp", "quot", "apos":
		default:
			if len(ent) < 2 || ent[0] != '#' {
				return errRef
			}
			var v rune
			digits, base := ent[1:], rune(10)
			if digits[0] == 'x' {
				digits, base = digits[1:], 16
			}
			if len(digits) == 0 {
				return errRef
			}
			for _, d := range digits {
				var x rune
				switch {
				case d >= '0' && d <= '9':
					x = rune(d - '0')
				case base == 16 && d >= 'a' && d <= 'f':
					x = rune(d-'a') + 10
				case base == 16 && d >= 'A' && d <= 'F':
					x = rune(d-'A') + 10
				default:
					return errRef
				}
				v = v*base + x
				if v > 0x10FFFF {
					return errRef
				}
			}
			if !isXMLChar(v) {
				return errRef
			}
		}
		i += e
	}
	return nil
}

// checkText checks character data between tags: references and no ]]>.
func checkText(b []byte) error {
	if bytes.Contains(b, []byte("]]>")) {
		return errCDEnd
	}
	return checkRefs(b)
}

// checkComment checks the inside of a comment: no -- and no - at the end.
func checkComment(b []byte) error {
	if bytes.Contains(b, []byte("--")) || len(b) > 0 && b[len(b)-1] == '-' {
		return errComment
	}
	return nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// declParser reads the pieces of an xml declaration without allocating.
type declParser struct {
	b []byte
	i int
}

func (d *declParser) spaces() int {
	n := 0
	for d.i < len(d.b) && isSpace(d.b[d.i]) {
		d.i++
		n++
	}
	return n
}

func (d *declParser) word(w string) bool {
	if len(d.b)-d.i >= len(w) && string(d.b[d.i:d.i+len(w)]) == w {
		d.i += len(w)
		return true
	}
	return false
}

// pair reads: name = "value" (or single quotes), and returns the value.
func (d *declParser) pair(name string) ([]byte, bool) {
	if !d.word(name) {
		return nil, false
	}
	d.spaces()
	if !d.word("=") {
		return nil, false
	}
	d.spaces()
	if d.i >= len(d.b) || (d.b[d.i] != '"' && d.b[d.i] != '\'') {
		return nil, false
	}
	q := d.b[d.i]
	e := bytes.IndexByte(d.b[d.i+1:], q)
	if e < 0 {
		return nil, false
	}
	v := d.b[d.i+1 : d.i+1+e]
	d.i += e + 2
	return v, true
}

func validEncName(b []byte) bool {
	if len(b) == 0 || !(b[0] >= 'A' && b[0] <= 'Z' || b[0] >= 'a' && b[0] <= 'z') {
		return false
	}
	for _, c := range b[1:] {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// parseDecl checks: xml version="1.0" [encoding="..."] [standalone="yes|no"], and returns the encoding name if there is one.
func parseDecl(inner []byte) (encoding []byte, ok bool) {
	d := declParser{b: inner}
	if !d.word("xml") || d.spaces() == 0 {
		return nil, false
	}
	if v, ok := d.pair("version"); !ok || string(v) != "1.0" {
		return nil, false
	}
	save := d.i
	if d.spaces() > 0 {
		if v, ok := d.pair("encoding"); ok {
			if !validEncName(v) {
				return nil, false
			}
			encoding = v
			save = d.i
		} else {
			d.i = save
		}
	}
	save = d.i
	if d.spaces() > 0 {
		if v, ok := d.pair("standalone"); ok {
			if string(v) != "yes" && string(v) != "no" {
				return nil, false
			}
			save = d.i
		} else {
			d.i = save
		}
	}
	d.i = save
	d.spaces()
	return encoding, d.i == len(d.b)
}

// checkPI checks a processing instruction <?target data?>, given the bytes between <? and ?>.
// atStart tells whether it is the first thing in the body, where the xml declaration is allowed.
func checkPI(inner []byte, atStart, bom bool) error {
	name := inner
	for i, c := range inner {
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			name = inner[:i]
			break
		}
	}
	if !validName(name) {
		return errPI
	}
	if bytes.EqualFold(name, []byte("xml")) {
		enc, ok := parseDecl(inner)
		if !atStart || !ok {
			return errDecl
		}
		if enc != nil {
			// The body is read as UTF-8 (or its ASCII part), so a declaration of a 16 or 32 bit encoding cannot be true.
			if bom && !strings.EqualFold(string(enc), "utf-8") && !strings.EqualFold(string(enc), "utf8") {
				return errDecl // a UTF-8 byte-order mark and another encoding in the declaration contradict each other
			}
			if hasPrefixFold(enc, "utf-16") || hasPrefixFold(enc, "utf-32") || hasPrefixFold(enc, "ucs-") {
				return errDecl
			}
		}
	}
	return nil
}

func hasPrefixFold(b []byte, p string) bool {
	return len(b) >= len(p) && strings.EqualFold(string(b[:len(p)]), p)
}
