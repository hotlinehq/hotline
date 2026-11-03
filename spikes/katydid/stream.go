package katydidcheck

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"unsafe"

	"go.katydid.org.za/parser-go/parse"
)

// streamParser reads XML bytes and hands katydid's matcher the same pieces its JSON reader does
// (enter, field, value, leave, with object and array marks), one at a time and without a tree.
// It follows the schema to know what each element is. It copies nothing from the body, except
// text that holds entities such as &amp;, which is decoded into a scratch buffer.
//
// Limits (see streaming-xml-requirements.md): repeated elements that are not side by side are an
// error, and schema choices (oneOf, anyOf, allOf) are read by taking the first member that names a property or type.
type streamParser struct {
	r    *resolver
	root schema

	body    []byte
	pos     int
	state   int
	scratch []byte
	numbuf  [8]byte

	q    []event
	head int
	last parse.Hint
	cur  event
	err  error // first error met while skipping, which the matcher ignores

	stack  []frame
	closed [][]byte
	attrs  []attr
}

const (
	stStart = iota
	stContent
	stDone
	stEOF
)

type event struct {
	hint parse.Hint
	kind parse.Kind
	s    []byte
	n    uint64
}

type frame struct {
	sch        schema
	name       []byte
	arr        []byte
	idx        int64
	closedBase int
}

type attr struct{ name, val []byte }

var (
	errDoctype   = errors.New("xml: DOCTYPE is not allowed")
	errSyntax    = errors.New("xml: not well-formed")
	errNotNear   = errors.New("xml: repeated element is not next to its siblings")
	errShape     = errors.New("xml: element holds elements where the schema expects text")
	errNestedArr = errors.New("xml: list inside a list is not supported")
)

func newStreamParser(r *resolver, root schema) *streamParser {
	return &streamParser{r: r, root: r.deref(root)}
}

// Init restarts the parser with a new body, reusing its buffers.
func (p *streamParser) Init(body []byte) {
	p.body, p.pos, p.state = body, 0, stStart
	p.q, p.head, p.last, p.err = p.q[:0], 0, parse.UnknownHint, nil
	p.stack, p.closed, p.attrs, p.scratch = p.stack[:0], p.closed[:0], p.attrs[:0], p.scratch[:0]
}

func (p *streamParser) Next() (parse.Hint, error) {
	for p.head >= len(p.q) {
		p.q, p.head, p.scratch = p.q[:0], 0, p.scratch[:0]
		if p.state == stEOF {
			return parse.UnknownHint, io.EOF
		}
		if err := p.fill(); err != nil {
			return parse.UnknownHint, err
		}
	}
	p.cur = p.q[p.head]
	p.head++
	p.last = p.cur.hint
	return p.cur.hint, nil
}

func (p *streamParser) Token() (parse.Kind, []byte, error) {
	e := &p.cur
	switch e.kind {
	case parse.Int64Kind, parse.Float64Kind:
		for i := range 8 {
			p.numbuf[i] = byte(e.n >> (8 * i))
		}
		return e.kind, p.numbuf[:], nil
	}
	return e.kind, e.s, nil
}

// Skip follows katydid's meaning: after enter or value it skips the rest of the container, after a field it skips the value.
func (p *streamParser) Skip() error {
	err := p.skip()
	if err != nil && p.err == nil {
		p.err = err
	}
	return err
}

// finish reads the rest of the body, so a body that is cut off or broken after the matcher
// has made up its mind (it skips the rest) is still reported as broken.
func (p *streamParser) finish() error {
	if p.err != nil {
		return p.err
	}
	for {
		if _, err := p.Next(); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
	}
}

func (p *streamParser) skip() error {
	switch p.last {
	case parse.LeaveHint, parse.UnknownHint:
		return nil
	case parse.FieldHint:
		h, err := p.Next()
		if err != nil || h != parse.EnterHint {
			return err
		}
	}
	depth := 0 // enters opened inside the container being skipped
	for {
		h, err := p.Next()
		if err != nil {
			return err
		}
		switch h {
		case parse.EnterHint:
			depth++
		case parse.LeaveHint:
			if depth == 0 {
				return nil
			}
			depth--
		}
	}
}

func (p *streamParser) push(h parse.Hint, k parse.Kind, s []byte, n uint64) {
	p.q = append(p.q, event{h, k, s, n})
}

func (p *streamParser) fieldName(name []byte) { p.push(parse.FieldHint, parse.StringKind, name, 0) }
func (p *streamParser) fieldIndex(i int64)    { p.push(parse.FieldHint, parse.Int64Kind, nil, uint64(i)) }
func (p *streamParser) enter()                { p.push(parse.EnterHint, parse.UnknownKind, nil, 0) }
func (p *streamParser) leave()                { p.push(parse.LeaveHint, parse.UnknownKind, nil, 0) }
func (p *streamParser) tag(t string) {
	p.push(parse.FieldHint, parse.TagKind, unsafe.Slice(unsafe.StringData(t), len(t)), 0)
}

// fill scans the body until at least one event is queued.
func (p *streamParser) fill() error {
	for len(p.q) == 0 {
		switch p.state {
		case stStart:
			if err := p.skipMisc(); err != nil {
				return err
			}
			if p.pos >= len(p.body) || p.body[p.pos] != '<' {
				return errSyntax
			}
			if err := p.startElement(true); err != nil {
				return err
			}
			p.state = stContent
			if len(p.stack) == 0 {
				p.state = stDone
			}
		case stContent:
			if err := p.content(); err != nil {
				return err
			}
			if len(p.stack) == 0 {
				p.state = stDone
			}
		case stDone:
			if err := p.skipMisc(); err != nil {
				return err
			}
			if p.pos < len(p.body) {
				return errSyntax
			}
			p.state = stEOF
			return nil
		default:
			return nil
		}
	}
	return nil
}

// skipMisc skips spaces, comments and processing lines (<?xml ...?>), and refuses a DOCTYPE.
func (p *streamParser) skipMisc() error {
	for p.pos < len(p.body) {
		switch c := p.body[p.pos]; {
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			p.pos++
		case bytes.HasPrefix(p.body[p.pos:], []byte("<?")):
			if err := p.skipTo("?>"); err != nil {
				return err
			}
		case bytes.HasPrefix(p.body[p.pos:], []byte("<!--")):
			if err := p.skipTo("-->"); err != nil {
				return err
			}
		case bytes.HasPrefix(p.body[p.pos:], []byte("<!")):
			return errDoctype
		default:
			return nil
		}
	}
	return nil
}

func (p *streamParser) skipTo(end string) error {
	i := bytes.Index(p.body[p.pos:], []byte(end))
	if i < 0 {
		return errSyntax
	}
	p.pos += i + len(end)
	return nil
}

// content handles markup inside an object element until it queues something.
func (p *streamParser) content() error {
	for len(p.q) == 0 {
		i := bytes.IndexByte(p.body[p.pos:], '<')
		if i < 0 {
			return errSyntax
		}
		p.pos += i
		rest := p.body[p.pos:]
		switch {
		case bytes.HasPrefix(rest, []byte("</")):
			name, n, err := readName(rest[2:])
			if err != nil {
				return err
			}
			top := &p.stack[len(p.stack)-1]
			if !bytes.Equal(localName(name), top.name) {
				return errSyntax
			}
			p.pos += 2 + n
			p.skipSpace()
			if p.pos >= len(p.body) || p.body[p.pos] != '>' {
				return errSyntax
			}
			p.pos++
			p.closeFrame()
			return nil
		case bytes.HasPrefix(rest, []byte("<!--")):
			if err := p.skipTo("-->"); err != nil {
				return err
			}
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			if err := p.skipTo("]]>"); err != nil {
				return err
			}
		case bytes.HasPrefix(rest, []byte("<?")):
			if err := p.skipTo("?>"); err != nil {
				return err
			}
		case bytes.HasPrefix(rest, []byte("<!")):
			return errDoctype
		default:
			if err := p.startElement(false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *streamParser) skipSpace() {
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case ' ', '\n', '\t', '\r':
			p.pos++
		default:
			return
		}
	}
}

func isNameEnd(c byte) bool {
	switch c {
	case ' ', '\n', '\t', '\r', '>', '/', '=':
		return true
	}
	return false
}

func readName(b []byte) (name []byte, n int, err error) {
	for n < len(b) && !isNameEnd(b[n]) {
		n++
	}
	if n == 0 {
		return nil, 0, errSyntax
	}
	return b[:n], n, nil
}

func localName(n []byte) []byte {
	if i := bytes.LastIndexByte(n, ':'); i >= 0 {
		return n[i+1:]
	}
	return n
}

// readStartTag reads <name attr="v" ...> or <name .../> at p.pos and fills p.attrs. xmlns attributes are dropped.
func (p *streamParser) readStartTag() (name []byte, selfClosing bool, err error) {
	name, n, err := readName(p.body[p.pos+1:])
	if err != nil {
		return nil, false, err
	}
	name = localName(name)
	p.pos += 1 + n
	p.attrs = p.attrs[:0]
	for {
		p.skipSpace()
		if p.pos >= len(p.body) {
			return nil, false, errSyntax
		}
		switch p.body[p.pos] {
		case '>':
			p.pos++
			return name, false, nil
		case '/':
			if p.pos+1 >= len(p.body) || p.body[p.pos+1] != '>' {
				return nil, false, errSyntax
			}
			p.pos += 2
			return name, true, nil
		}
		an, k, err := readName(p.body[p.pos:])
		if err != nil {
			return nil, false, err
		}
		p.pos += k
		p.skipSpace()
		if p.pos >= len(p.body) || p.body[p.pos] != '=' {
			return nil, false, errSyntax
		}
		p.pos++
		p.skipSpace()
		if p.pos >= len(p.body) || (p.body[p.pos] != '"' && p.body[p.pos] != '\'') {
			return nil, false, errSyntax
		}
		q := p.body[p.pos]
		e := bytes.IndexByte(p.body[p.pos+1:], q)
		if e < 0 {
			return nil, false, errSyntax
		}
		val := p.body[p.pos+1 : p.pos+1+e]
		p.pos += e + 2
		if bytes.Equal(an, []byte("xmlns")) || bytes.HasPrefix(an, []byte("xmlns:")) {
			continue
		}
		p.attrs = append(p.attrs, attr{localName(an), val})
	}
}

// startElement handles a start tag: the root (root true) or a child of the open object.
func (p *streamParser) startElement(root bool) error {
	name, selfClosing, err := p.readStartTag()
	if err != nil {
		return err
	}
	if root {
		p.enter()
		p.tag("object")
		p.enter()
		p.stack = append(p.stack, frame{sch: p.root, name: name, closedBase: len(p.closed)})
		p.emitAttrs()
		if selfClosing {
			p.closeFrame()
		}
		return nil
	}
	f := &p.stack[len(p.stack)-1]
	ps := p.r.prop(f.sch, name)
	target := ps
	if p.r.typeOf(ps) == "array" {
		target = p.r.deref(p.r.items(ps))
		if p.r.typeOf(target) == "array" {
			return errNestedArr
		}
		if f.arr != nil && bytes.Equal(f.arr, name) {
			f.idx++
		} else {
			p.closeArray(f)
			for _, c := range p.closed[f.closedBase:] {
				if bytes.Equal(c, name) {
					return errNotNear
				}
			}
			p.fieldName(name)
			p.enter()
			p.tag("array")
			p.enter()
			f.arr, f.idx = name, 0
		}
		p.fieldIndex(f.idx)
	} else {
		p.closeArray(f)
		p.fieldName(name)
	}
	isObject := false
	switch t := p.r.typeOf(target); t {
	case "object":
		isObject = true
	case "":
		isObject = !selfClosing && p.nextIsElement()
	}
	if isObject {
		p.enter()
		p.tag("object")
		p.enter()
		p.stack = append(p.stack, frame{sch: target, name: name, closedBase: len(p.closed)})
		p.emitAttrs()
		if selfClosing {
			p.closeFrame()
		}
		return nil
	}
	return p.scalar(name, selfClosing, target)
}

// nextIsElement looks ahead past spaces: is the first thing inside the element another element?
func (p *streamParser) nextIsElement() bool {
	i := p.pos
	for i < len(p.body) && (p.body[i] == ' ' || p.body[i] == '\n' || p.body[i] == '\t' || p.body[i] == '\r') {
		i++
	}
	if i+1 >= len(p.body) || p.body[i] != '<' {
		return false
	}
	switch p.body[i+1] {
	case '/', '!', '?':
		return false
	}
	return true
}

func (p *streamParser) closeArray(f *frame) {
	if f.arr == nil {
		return
	}
	p.leave()
	p.leave()
	p.closed = append(p.closed, f.arr)
	f.arr = nil
}

func (p *streamParser) closeFrame() {
	f := &p.stack[len(p.stack)-1]
	p.closeArray(f)
	p.leave()
	p.leave()
	p.closed = p.closed[:f.closedBase]
	p.stack = p.stack[:len(p.stack)-1]
}

// emitAttrs queues the attributes of the element just opened as fields of its object.
func (p *streamParser) emitAttrs() {
	f := &p.stack[len(p.stack)-1]
	for _, a := range p.attrs {
		p.fieldName(a.name)
		p.value(p.r.typeOf(p.r.prop(f.sch, a.name)), p.decode(a.val))
	}
}

// scalar reads the text of an element up to its end tag and queues one value.
func (p *streamParser) scalar(name []byte, selfClosing bool, target schema) error {
	if selfClosing {
		p.value(p.r.typeOf(target), nil)
		return nil
	}
	var text []byte
	multi := false
	for {
		i := bytes.IndexByte(p.body[p.pos:], '<')
		if i < 0 {
			return errSyntax
		}
		chunk := p.body[p.pos : p.pos+i]
		p.pos += i
		if len(chunk) > 0 {
			if text == nil && !multi {
				text = p.decode(chunk)
			} else {
				if !multi {
					p.scratch = append(p.scratch[:0:0], text...)
					multi = true
				}
				p.scratch = append(p.scratch, p.decode(chunk)...)
				text = p.scratch
			}
		}
		rest := p.body[p.pos:]
		switch {
		case bytes.HasPrefix(rest, []byte("</")):
			en, n, err := readName(rest[2:])
			if err != nil || !bytes.Equal(localName(en), name) {
				return errSyntax
			}
			p.pos += 2 + n
			p.skipSpace()
			if p.pos >= len(p.body) || p.body[p.pos] != '>' {
				return errSyntax
			}
			p.pos++
			p.value(p.r.typeOf(target), text)
			return nil
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			e := bytes.Index(rest, []byte("]]>"))
			if e < 0 {
				return errSyntax
			}
			if !multi {
				p.scratch = append(p.scratch[:0:0], text...)
				multi = true
			}
			p.scratch = append(p.scratch, rest[9:e]...)
			text = p.scratch
			p.pos += e + 3
			if text == nil {
				text = []byte{}
			}
		case bytes.HasPrefix(rest, []byte("<!--")):
			if err := p.skipTo("-->"); err != nil {
				return err
			}
		case bytes.HasPrefix(rest, []byte("<?")):
			if err := p.skipTo("?>"); err != nil {
				return err
			}
		case bytes.HasPrefix(rest, []byte("<!")):
			return errDoctype
		default:
			return errShape
		}
	}
}

// decode returns the text with entities replaced. Text without '&' is returned as it is, not copied.
func (p *streamParser) decode(b []byte) []byte {
	if bytes.IndexByte(b, '&') < 0 {
		return b
	}
	start := len(p.scratch)
	for i := 0; i < len(b); i++ {
		if b[i] != '&' {
			p.scratch = append(p.scratch, b[i])
			continue
		}
		e := bytes.IndexByte(b[i:], ';')
		if e < 0 {
			p.scratch = append(p.scratch, b[i:]...)
			break
		}
		ent := b[i+1 : i+e]
		switch string(ent) {
		case "lt":
			p.scratch = append(p.scratch, '<')
		case "gt":
			p.scratch = append(p.scratch, '>')
		case "amp":
			p.scratch = append(p.scratch, '&')
		case "quot":
			p.scratch = append(p.scratch, '"')
		case "apos":
			p.scratch = append(p.scratch, '\'')
		default:
			if len(ent) > 1 && ent[0] == '#' {
				var v uint64
				var err error
				if ent[1] == 'x' || ent[1] == 'X' {
					v, err = strconv.ParseUint(string(ent[2:]), 16, 32)
				} else {
					v, err = strconv.ParseUint(string(ent[1:]), 10, 32)
				}
				if err == nil {
					p.scratch = append(p.scratch, string(rune(v))...)
					break
				}
			}
			p.scratch = append(p.scratch, b[i:i+e+1]...)
		}
		i += e
	}
	return p.scratch[start:]
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\n' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(unsafe.String(&b[0], len(b)), 10, 64)
	return v, err == nil
}

// parseFloat accepts plain decimal spellings only (digits, sign, dot, exponent), as JSON does:
// not NaN, Inf, hex floats or underscores, which Go's own parser would take.
func parseFloat(b []byte) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	for _, c := range b {
		if (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' && c != 'e' && c != 'E' {
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(unsafe.String(&b[0], len(b)), 64)
	return v, err == nil
}

// value queues a value typed by the schema's type. Text that does not fit the type stays text, so the matcher refuses it.
func (p *streamParser) value(typ string, text []byte) {
	t := trimSpace(text)
	switch typ {
	case "integer":
		if v, ok := parseInt(t); ok {
			p.push(parse.ValueHint, parse.Int64Kind, nil, uint64(v))
			return
		}
	case "number":
		if v, ok := parseFloat(t); ok {
			p.push(parse.ValueHint, parse.Float64Kind, nil, float64bits(v))
			return
		}
	case "boolean":
		switch string(t) {
		case "true":
			p.push(parse.ValueHint, parse.TrueKind, nil, 0)
			return
		case "false":
			p.push(parse.ValueHint, parse.FalseKind, nil, 0)
			return
		}
	}
	p.push(parse.ValueHint, parse.StringKind, text, 0)
}
