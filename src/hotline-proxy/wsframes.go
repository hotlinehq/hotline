package proxy

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A WebSocket's frames, read as they pass (RFC 6455). Each way of a
// session whose route lists its messages is a session side. The bytes pass on at
// once; a copy of them waits for the side's reader, which reads the frames,
// unpacks them when they are compressed, and hands each whole message on
// to be named, checked and recorded (Peter, 2026-11-19, WebSocket slice 2).

var (
	// errLost says the copy of a session side's bytes stopped, because it could not
	// fit in the check line's memory.
	errLost = errors.New("websocket: the copy of the messages did not fit in specChecks.queueMemory")
	// errBrokenFrames says the bytes are not WebSocket frames the proxy can
	// read.
	errBrokenFrames = errors.New("websocket: the frames can't be read")
)

// The opcodes of RFC 6455, section 5.2.
const (
	opContinue = 0x0
	opText     = 0x1
	opBinary   = 0x2
	opClose    = 0x8
	opPing     = 0x9
	opPong     = 0xa
)

// seenMessage is a message whose last byte has passed: its place among
// the session's messages, both ways together, and when its first and last
// bytes passed.
type seenMessage struct {
	index      int64
	start, end time.Time
}

// chunk is a copy of bytes that passed, waiting for the reader, and the
// line's memory it holds.
type chunk struct {
	data []byte
	off  int
	held int64
}

// sessionSide is one way of a session: what the app sends, or what the vendor
// sends.
type sessionSide struct {
	r       *messageReader
	fromApp bool
	// unpack is set when the 101 agreed to permessage-deflate.
	unpack *inflater
	done   chan struct{}

	mu   sync.Mutex
	more *sync.Cond
	scan frameScanner
	// seen are the messages whose last byte passed, not yet read.
	seen   []seenMessage
	chunks []chunk
	// busy says the reader is reading, not waiting for bytes.
	busy  bool
	ended bool
	// lost says the copy stopped; the messages from then on are recorded
	// as overloaded.
	lost bool

	// pings and pongs are read by the reader alone, and by the session
	// record once done is closed.
	pings, pongs int64
	buf          []byte
}

func newSessionSide(r *messageReader, fromApp bool, unpack *inflater) *sessionSide {
	w := &sessionSide{r: r, fromApp: fromApp, unpack: unpack, done: make(chan struct{})}
	w.more = sync.NewCond(&w.mu)
	go w.read()
	return w
}

// pass is told each piece of bytes as it passes. It only copies them: a
// reader that is waiting takes the copy whatever its size, as a free
// check worker does; one that is behind holds it in the check line's
// memory, and when that is full, the copy stops for good (Peter,
// 2026-11-19, Q17 and Q26). It also finds where each message ends, so the
// messages are numbered in the order they passed.
func (w *sessionSide) pass(p []byte) {
	if len(p) == 0 {
		return
	}
	at := w.r.c.h.clock.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.scan.feed(p, at, func(start time.Time) {
		w.seen = append(w.seen, seenMessage{index: w.r.messageCount.Add(1), start: start, end: at})
	})
	defer w.more.Signal()
	if w.lost {
		return
	}
	var held int64
	if w.busy || len(w.chunks) > 0 {
		if !w.r.c.h.checks.reserve(int64(len(p))) {
			w.lost = true
			return
		}
		held = int64(len(p))
	}
	w.chunks = append(w.chunks, chunk{data: bytes.Clone(p), held: held})
}

// end says no more bytes come, which the forwarder makes sure of: the
// reader reads what is left and stops.
func (w *sessionSide) end() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	w.more.Signal()
}

// Read gives the reader the copied bytes, in order, waiting for more.
func (w *sessionSide) Read(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.chunks) == 0 {
		w.busy = false
		switch {
		case w.lost:
			return 0, errLost
		case w.ended:
			return 0, io.EOF
		}
		w.more.Wait()
	}
	w.busy = true
	c := &w.chunks[0]
	n := copy(p, c.data[c.off:])
	c.off += n
	if c.off == len(c.data) {
		w.r.c.h.checks.release(c.held)
		w.chunks[0] = chunk{}
		w.chunks = w.chunks[1:]
	}
	return n, nil
}

// read reads the side's messages until it ends, then records the
// messages it could not read.
func (w *sessionSide) read() {
	defer close(w.done)
	fr := &frameReader{in: w, control: w.control}
	err := w.messages(fr)
	reason := ""
	if errors.Is(err, errLost) {
		reason = "overloaded"
	}
	w.mu.Lock()
	// Bytes still coming are not kept: nothing reads them.
	w.lost = true
	w.release()
	for {
		for len(w.seen) > 0 {
			seen := w.seen[0]
			w.seen = w.seen[1:]
			w.mu.Unlock()
			w.r.unread(w, seen, reason)
			w.mu.Lock()
		}
		if w.ended {
			w.mu.Unlock()
			return
		}
		w.more.Wait()
	}
}

// release gives back the memory of the copies still waiting. It is
// called locked.
func (w *sessionSide) release() {
	for _, c := range w.chunks {
		w.r.c.h.checks.release(c.held)
	}
	w.chunks = nil
}

// messages reads each message and hands it on, until the bytes end or
// can't be read.
func (w *sessionSide) messages(fr *frameReader) error {
	if w.buf == nil {
		w.buf = make([]byte, 32<<10)
	}
	for {
		f, err := fr.message()
		if err != nil {
			return err
		}
		if f.rsv1 && w.unpack == nil {
			return errBrokenFrames
		}
		body := &messageBody{fr: fr, fin: f.fin}
		kept := &keptMessage{limit: keptBody}
		if f.rsv1 {
			err = w.unpack.unpack(body, kept, w.buf)
		} else {
			_, err = io.CopyBuffer(kept, body, w.buf)
		}
		if err != nil {
			return err
		}
		// The frame scanner saw this message end before its bytes were copied, so
		// it is first among those seen.
		w.mu.Lock()
		seen := w.seen[0]
		w.seen = w.seen[1:]
		w.mu.Unlock()
		w.r.message(w, seen, message{data: kept.data, size: body.size, unpacked: kept.size, packed: f.rsv1})
	}
}

// control handles a control frame: pings and pongs are counted, and the
// session's first close message is kept.
func (w *sessionSide) control(op byte, payload []byte) {
	switch op {
	case opPing:
		w.pings++
	case opPong:
		w.pongs++
	case opClose:
		w.r.closed(w.fromApp, payload)
	}
}

// message is one whole message, as read: the part kept for the check, its
// size as sent, and its size once unpacked.
type message struct {
	data     []byte
	size     int64
	unpacked int64
	packed   bool
}

// keptMessage keeps the first limit bytes of a message, and counts them
// all.
type keptMessage struct {
	data  []byte
	size  int64
	limit int
}

func (k *keptMessage) Write(p []byte) (int, error) {
	k.size += int64(len(p))
	if room := k.limit - len(k.data); room > 0 {
		k.data = append(k.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// frame is one frame's header.
type frame struct {
	fin    bool
	rsv1   bool
	op     byte
	length int64
	masked bool
	mask   [4]byte
}

// frameReader reads frames from a session side's copy, and hands each control
// frame to control.
type frameReader struct {
	in      io.Reader
	control func(op byte, payload []byte)
	head    [14]byte
	// left is what is still to read of the frame's payload, and at how far
	// into it the reader is, for the mask.
	f    frame
	left int64
	at   int64
}

// header reads the next frame's header.
func (fr *frameReader) header() (frame, error) {
	var f frame
	if _, err := io.ReadFull(fr.in, fr.head[:2]); err != nil {
		return f, err
	}
	size := headerSize(fr.head[1])
	if _, err := io.ReadFull(fr.in, fr.head[2:size]); err != nil {
		return f, err
	}
	f.fin, f.rsv1, f.op = fr.head[0]&0x80 != 0, fr.head[0]&0x40 != 0, fr.head[0]&0x0f
	length := payloadLength(fr.head[:size])
	if length > math.MaxInt64 {
		// RFC 6455 keeps the top bit 0.
		return f, errBrokenFrames
	}
	f.length = int64(length)
	if f.masked = fr.head[1]&0x80 != 0; f.masked {
		copy(f.mask[:], fr.head[size-4:size])
	}
	return f, nil
}

// start begins reading a frame's payload.
func (fr *frameReader) start(f frame) {
	fr.f, fr.left, fr.at = f, f.length, 0
}

// Read reads the frame's payload, unmasked. Its callers read only while
// some is left.
func (fr *frameReader) Read(p []byte) (int, error) {
	n, err := fr.in.Read(p[:min(int64(len(p)), fr.left)])
	if fr.f.masked {
		for i := range n {
			p[i] ^= fr.f.mask[(fr.at+int64(i))%4]
		}
	}
	fr.left -= int64(n)
	fr.at += int64(n)
	return n, err
}

// message reads up to the first frame of the next message, handling the
// control frames before it.
func (fr *frameReader) message() (frame, error) {
	for {
		f, err := fr.header()
		if err != nil {
			return f, err
		}
		switch {
		case f.op >= opClose:
			if err := fr.controlFrame(f); err != nil {
				return f, err
			}
		case f.op == opText || f.op == opBinary:
			fr.start(f)
			return f, nil
		default:
			return f, errBrokenFrames
		}
	}
}

// controlFrame reads a control frame's payload, at most 125 bytes, and
// hands it on.
func (fr *frameReader) controlFrame(f frame) error {
	if f.length > 125 || !f.fin {
		return errBrokenFrames
	}
	fr.start(f)
	var payload [125]byte
	if _, err := io.ReadFull(fr, payload[:f.length]); err != nil {
		return err
	}
	fr.control(f.op, payload[:f.length])
	return nil
}

// messageBody reads one message's payload across its frames, handling the
// control frames between them.
type messageBody struct {
	fr   *frameReader
	fin  bool
	size int64
}

func (b *messageBody) Read(p []byte) (int, error) {
	for b.fr.left == 0 {
		if b.fin {
			return 0, io.EOF
		}
		if err := b.next(); err != nil {
			return 0, unexpected(err)
		}
	}
	n, err := b.fr.Read(p)
	b.size += int64(n)
	return n, unexpected(err)
}

// next reads the message's next frame, handling a control frame before
// it.
func (b *messageBody) next() error {
	f, err := b.fr.header()
	switch {
	case err != nil:
		return err
	case f.op >= opClose:
		return b.fr.controlFrame(f)
	case f.op != opContinue:
		return errBrokenFrames
	}
	b.fr.start(f)
	b.fin = f.fin
	return nil
}

// unexpected is an error inside a message: the session side ending there is not
// the message's end.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// frameScanner finds where each message ends as the bytes pass, from the frame
// headers alone. It neither unmasks nor keeps anything.
type frameScanner struct {
	head    [14]byte
	have    int
	need    int
	left    uint64
	payload bool
	// inMessage says a data message has begun and not ended; started is
	// when its first byte passed.
	inMessage bool
	started   time.Time
}

// feed reads p, which passed at at, and calls ended for each message
// whose last byte is in it.
func (s *frameScanner) feed(p []byte, at time.Time, ended func(start time.Time)) {
	for len(p) > 0 {
		if s.payload {
			n := min(s.left, uint64(len(p)))
			p, s.left = p[n:], s.left-n
			if s.left == 0 {
				s.frameEnd(ended)
			}
			continue
		}
		if s.have == 0 {
			s.need = 2
			if op := p[0] & 0x0f; op < opClose && !s.inMessage {
				s.inMessage, s.started = true, at
			}
		}
		n := copy(s.head[s.have:s.need], p)
		p, s.have = p[n:], s.have+n
		if s.have < s.need {
			continue
		}
		if s.need == 2 {
			s.need = headerSize(s.head[1])
			if s.have < s.need {
				continue
			}
		}
		s.left = payloadLength(s.head[:s.need])
		s.payload = true
		if s.left == 0 {
			s.frameEnd(ended)
		}
	}
}

// headerSize is how long a frame's header is, from its second byte: the
// length's own bytes and the mask's.
func headerSize(second byte) int {
	size := 2
	switch second & 0x7f {
	case 126:
		size += 2
	case 127:
		size += 8
	}
	if second&0x80 != 0 {
		size += 4
	}
	return size
}

// payloadLength is a frame's payload length, from its whole header.
func payloadLength(head []byte) uint64 {
	switch length := head[1] & 0x7f; length {
	case 126:
		return uint64(binary.BigEndian.Uint16(head[2:4]))
	case 127:
		return binary.BigEndian.Uint64(head[2:10])
	default:
		return uint64(length)
	}
}

// frameEnd is the end of a frame: the message ends with a final data or
// continuation frame.
func (s *frameScanner) frameEnd(ended func(start time.Time)) {
	s.payload, s.have = false, 0
	if s.head[0]&0x0f < opClose && s.head[0]&0x80 != 0 {
		s.inMessage = false
		ended(s.started)
	}
}

// inflater unpacks one way's compressed messages (RFC 7692). When the way
// packs against earlier messages, it keeps the last window bytes unpacked
// for as long as the session is open, 32 KB unless the 101 agreed a
// smaller window (Peter, 2026-11-19, Q6).
type inflater struct {
	takeover bool
	window   int
	dict     []byte
	reader   io.ReadCloser
}

// deflateTail ends a message's packed bytes as RFC 7692 says the sender
// left them out, then adds an empty last block, so the unpacking stops at
// the message's end.
func deflateTail() []byte {
	return []byte{0x00, 0x00, 0xff, 0xff, 0x01, 0x00, 0x00, 0xff, 0xff}
}

// unpack unpacks one message into out. Every message is unpacked, whatever
// out keeps of it, so the next one can be (Q6).
func (d *inflater) unpack(body io.Reader, out io.Writer, buf []byte) error {
	src := io.MultiReader(body, bytes.NewReader(deflateTail()))
	var dict []byte
	if d.takeover {
		dict = d.dict[max(0, len(d.dict)-d.window):]
	}
	if d.reader == nil {
		d.reader = flate.NewReaderDict(src, dict)
	} else {
		// Reset of the standard library's reader never fails.
		_ = d.reader.(flate.Resetter).Reset(src, dict)
	}
	if d.takeover {
		out = io.MultiWriter(out, d)
	}
	if _, err := io.CopyBuffer(out, d.reader, buf); err != nil {
		return errors.Join(errBrokenFrames, err)
	}
	// A message that ended its own packed stream may leave bytes after it.
	_, err := io.CopyBuffer(io.Discard, body, buf)
	return err
}

// Write keeps the last window bytes unpacked, in one list of twice the
// window made once per session: new bytes go after the kept ones, and
// only when the list is full are the last window bytes moved to its
// start. A new list for every message handed out most of a compressed
// session's memory (load test, 2026-11-20).
func (d *inflater) Write(p []byte) (int, error) {
	n := len(p)
	if n >= d.window {
		p = p[n-d.window:]
	}
	if d.dict == nil {
		d.dict = make([]byte, 0, 2*d.window)
	}
	if len(d.dict)+len(p) > cap(d.dict) {
		keep := d.dict[max(0, len(d.dict)-(d.window-len(p))):]
		d.dict = d.dict[:copy(d.dict[:cap(d.dict)], keep)]
	}
	d.dict = append(d.dict, p...)
	return n, nil
}

// deflateAgreed reads the permessage-deflate the 101 agreed to, if any, as
// the inflaters of each way: the app's, then the vendor's.
func deflateAgreed(extensions []string) (app, vendor *inflater) {
	for _, offer := range strings.Split(strings.Join(extensions, ","), ",") {
		params := strings.Split(offer, ";")
		if !strings.EqualFold(strings.TrimSpace(params[0]), "permessage-deflate") {
			continue
		}
		app = &inflater{takeover: true, window: 1 << 15}
		vendor = &inflater{takeover: true, window: 1 << 15}
		for _, param := range params[1:] {
			name, value, _ := strings.Cut(strings.TrimSpace(param), "=")
			bits, err := strconv.Atoi(strings.Trim(value, `"`))
			sized := err == nil && bits >= 8 && bits <= 15
			switch strings.ToLower(name) {
			case "client_no_context_takeover":
				app.takeover = false
			case "server_no_context_takeover":
				vendor.takeover = false
			case "client_max_window_bits":
				if sized {
					app.window = 1 << bits
				}
			case "server_max_window_bits":
				if sized {
					vendor.window = 1 << bits
				}
			}
		}
		return app, vendor
	}
	return nil, nil
}
