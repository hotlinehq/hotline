// Package queuefiles keeps the items of one signal of an exporter's
// queue as files in a folder: numbered queue files written one after
// another, and a position file saying where the sender is. The export
// queue uses it for what does not fit in memory (Peter, 2026-11-27).
package queuefiles

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hotline-proxy/internal/fits"
)

// The disk queue is the part of an OTLP exporter's queue kept as files in
// its queueFolder, used once the memory queue is full and until it is
// Empty again (Peter, 2026-11-27). Each signal has its own subfolder, a
// Folder here. The design follows Vector's disk buffer:
// items as opaque bytes, each framed with its length and a CRC32C, whole
// files deleted once every item in them was taken, and a sync to disk at
// most every 500 ms.

const (
	// queueFileSize is the most bytes one queue file holds: small, so
	// space comes back soon and the smallest queueDisk stays small
	// (Peter, 2026-11-27).
	queueFileSize = 16 << 20
	// queueMagic starts every queue file, then its format version.
	queueMagic   = "HTLQ"
	queueVersion = 1
	// queueHeaderSize is the magic, the version and the number of the
	// file's first item.
	queueHeaderSize = 4 + 1 + 8
	// frameHeaderSize is an item's length, crc, kind and the time it was
	// queued, before its payload.
	frameHeaderSize = 4 + 4 + 1 + 8
	// positionSize is the position file: the queue file's number, the
	// offset and number of the next item there, and a crc of the three.
	positionSize = 4 + 8 + 8 + 4
	// The numbers start high, so a stop can put the items still in
	// memory in a file before the oldest one, in order.
	firstFileNumber = 1 << 20
	firstItemNumber = 1 << 40
	positionFile    = "position"
)

// Kind says what an item on disk holds.
type Kind byte

const (
	// Record is one log record.
	Record Kind = 1
	// BatchRecord is one of the ledger's batch records.
	BatchRecord Kind = 2
	// Spans is one call's spans, kept together.
	Spans Kind = 3
)

// checksum is the CRC32C of b. The library makes the Castagnoli table once
// and hands back the same one each time.
func checksum(b []byte) uint32 { return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)) }

// errQueueFile is an item on disk that cannot be read: a wrong crc, or
// cut short.
var errQueueFile = errors.New("queuefiles: a queue file item cannot be read")

// errManyParts is a record or batch record item with more than its one
// part, which is never written: only its first part would be kept.
var errManyParts = errors.New("queuefiles: a record or batch record item has more than one part")

// Budget is the bytes one exporter's disk queues may hold in all,
// records and spans together: what waits to be written and what the
// queue files hold. Every Folder of the exporter shares it.
type Budget struct {
	limit int64
	used  atomic.Int64
}

// NewBudget is a budget of limit bytes, none of them used.
func NewBudget(limit int64) *Budget { return &Budget{limit: limit} }

// Take reserves n bytes, and says whether they fit.
func (b *Budget) Take(n int64) bool {
	if b.used.Add(n) > b.limit {
		b.used.Add(-n)
		return false
	}
	return true
}

// TakeAnyway reserves n bytes even past the limit, for an item that is
// never refused.
func (b *Budget) TakeAnyway(n int64) { b.used.Add(n) }

// GiveBack frees n bytes reserved for an item that was not written.
func (b *Budget) GiveBack(n int64) { b.used.Add(-n) }

// queueFile is one queue file: its number, the number of its first item,
// and its size in bytes, all of it written.
type queueFile struct {
	number uint32
	first  uint64
	size   int64
}

// Position is where the sender is: a queue file, the offset of its next
// item there and that item's number. Read gives one; Taken moves the
// sender there.
type Position struct {
	number uint32
	offset int64
	item   uint64
}

// frame is one item waiting to be written to disk, and the time it was
// first queued, in memory or on disk.
type frame struct {
	kind    Kind
	payload []byte
	queued  time.Time
}

// Item is one item on disk: a record or a batch record, as its one part,
// or one call's spans, each span a part. Queued is when it was first
// queued, in memory or on disk.
type Item struct {
	Kind   Kind
	Parts  [][]byte
	Queued time.Time
}

// Size is the bytes the item takes on disk, which a Budget must hold
// before the item is written.
func (it Item) Size() int64 { return frameSize(it.payload()) }

// payload is the bytes written for the item: one call's spans joined, or
// the one part of a record.
func (it Item) payload() []byte {
	if it.Kind == Spans {
		return joinSpans(it.Parts)
	}
	if len(it.Parts) == 0 {
		return nil
	}
	return it.Parts[0]
}

// frames are items as they are written. A record with more than one part
// is refused before any item is written, so no part is lost unseen.
func frames(items []Item) ([]frame, error) {
	out := make([]frame, len(items))
	for i, it := range items {
		if it.Kind != Spans && len(it.Parts) > 1 {
			return nil, errManyParts
		}
		out[i] = frame{kind: it.Kind, payload: it.payload(), queued: it.Queued}
	}
	return out, nil
}

// Folder is the queue files of one signal. One writer appends and syncs;
// the sender reads, and moves its Position once the collector took what
// it read. mu guards files and next; each side owns its own file handles.
type Folder struct {
	dir string
	// root opens the files inside dir only.
	root   *os.Root
	budget *Budget

	mu sync.Mutex
	// files are the queue files, oldest first; the last is being written.
	files []queueFile
	// next is the number of the next item written.
	next uint64
	// lastReadQueued is when the sender's next item was queued, as it last
	// Read it; zero once the sender has handed on everything.
	lastReadQueued time.Time

	// The writer's side.
	w     *os.File
	dirty bool

	// The sender's side.
	at       Position
	r        *os.File
	rNumber  uint32
	pos      *os.File
	posDirty atomic.Bool
}

// queueFileName is a queue file's name: its number, so a plain sort gives
// the oldest first.
func queueFileName(number uint32) string { return fmt.Sprintf("%08d.queue", number) }

// Open opens the queue files in dir, or starts them. It reads the
// position file; when that cannot be read the sender starts at the oldest
// file, and the collector may get some items twice. A cut-off item at the
// end of the newest file, from a crash while it was written, is cut away
// and counted in lost, as is a file with no readable header. New items
// always go to a new file.
func Open(dir string, budget *Budget) (folder *Folder, lost int, err error) {
	var root *os.Root
	err = os.MkdirAll(dir, 0o700)
	if err == nil {
		root, err = os.OpenRoot(dir)
	}
	if err != nil {
		return nil, 0, err
	}
	d := &Folder{dir: dir, root: root, budget: budget}
	at, known := readPosition(root)
	for _, number := range listQueueFiles(root) {
		f, err := statQueueFile(root, number)
		switch {
		case known && number < at.number:
			// Taken before the stop; only its deletion was missed.
		case err != nil:
			lost++
		default:
			d.files = append(d.files, f)
			budget.used.Add(f.size)
			continue
		}
		// What cannot be removed stays, and is skipped again next time.
		_ = root.Remove(queueFileName(number))
	}
	number, first := uint32(firstFileNumber), uint64(firstItemNumber)
	d.next = first
	if n := len(d.files); n > 0 {
		lost += d.cutTail()
		number, first = d.files[n-1].number+1, d.next
	}
	if err := d.startFile(number, first); err != nil {
		_ = root.Close()
		return nil, 0, err
	}
	if !known || !slices.ContainsFunc(d.files, func(f queueFile) bool { return f.number == at.number }) {
		at = Position{number: d.files[0].number, offset: queueHeaderSize, item: d.files[0].first}
	}
	// The folder could be written to just now, so the position file can
	// be made.
	d.pos, _ = root.OpenFile(positionFile, os.O_CREATE|os.O_RDWR, 0o600)
	d.writePosition(at)
	return d, lost, nil
}

// cutTail finds where the newest queue file's last whole item ends, and
// cuts away anything after it: an item a crash cut short. It counts that
// item as lost, and sets the number of the next item.
func (d *Folder) cutTail() int {
	last := &d.files[len(d.files)-1]
	name := queueFileName(last.number)
	// statQueueFile has just read the file.
	data, _ := d.root.ReadFile(name)
	offset, count := int64(queueHeaderSize), uint64(0)
	for {
		_, n, err := readFrame(data[offset:])
		if err != nil {
			break
		}
		offset += n
		count++
	}
	d.next = last.first + count
	if offset == last.size {
		return 0
	}
	// A file that cannot be cut is read up to its broken item, and the
	// rest is skipped as unreadable then.
	if f, err := d.root.OpenFile(name, os.O_WRONLY, 0); err == nil {
		_ = f.Truncate(offset)
		_ = f.Close()
	}
	d.budget.TakeAnyway(offset - last.size)
	last.size = offset
	return 1
}

// listQueueFiles are the numbers of the queue files in root, oldest
// first. Open has just opened it, so it can be read.
func listQueueFiles(root *os.Root) []uint32 {
	entries, _ := fs.ReadDir(root.FS(), ".")
	var numbers []uint32
	for _, e := range entries {
		name, found := strings.CutSuffix(e.Name(), ".queue")
		if n, err := strconv.ParseUint(name, 10, 32); found && err == nil {
			numbers = append(numbers, uint32(n))
		}
	}
	slices.Sort(numbers)
	return numbers
}

// statQueueFile reads a queue file's header and size. A file that cannot
// be opened or has no whole header holds nothing that can be read.
func statQueueFile(root *os.Root, number uint32) (queueFile, error) {
	f, err := root.Open(queueFileName(number))
	if err != nil {
		return queueFile{}, err
	}
	defer func() { _ = f.Close() }()
	var header [queueHeaderSize]byte
	_, err = io.ReadFull(f, header[:])
	if err != nil || string(header[:4]) != queueMagic || header[4] != queueVersion {
		return queueFile{}, errQueueFile
	}
	// The file was read, so it has a size.
	size, _ := f.Seek(0, io.SeekEnd)
	return queueFile{number: number, first: binary.LittleEndian.Uint64(header[5:]), size: size}, nil
}

// readFrame reads one item at the start of data, and how many bytes it
// takes. Its payload points into data.
func readFrame(data []byte) (frame, int64, error) {
	if len(data) < frameHeaderSize {
		return frame{}, 0, errQueueFile
	}
	length := int64(binary.LittleEndian.Uint32(data))
	if int64(len(data)) < frameHeaderSize+length {
		return frame{}, 0, errQueueFile
	}
	body := data[8 : frameHeaderSize+length]
	if checksum(body) != binary.LittleEndian.Uint32(data[4:]) {
		return frame{}, 0, errQueueFile
	}
	// A time past the year 2262 cannot be written, so it cannot be read.
	nanos, _ := fits.Int64(binary.LittleEndian.Uint64(body[1:]))
	queued := time.Unix(0, nanos)
	return frame{kind: Kind(body[0]), payload: body[9:], queued: queued}, frameHeaderSize + length, nil
}

// appendFrame writes one item: length, crc, kind, the time it was queued
// and payload. The crc covers all but the length and itself.
func appendFrame(b []byte, item frame) []byte {
	// An item is far below 4 GiB: what goes to disk is refused above
	// unwrittenMemory, and what goes from memory at a stop was one call's.
	length, _ := fits.Uint32(len(item.payload))
	b = binary.LittleEndian.AppendUint32(b, length)
	start := len(b)
	b = append(b, 0, 0, 0, 0, byte(item.kind))
	// A time before 1970 is written as 1970.
	nanos, _ := fits.Uint64(int(item.queued.UnixNano()))
	b = binary.LittleEndian.AppendUint64(b, nanos)
	b = append(b, item.payload...)
	binary.LittleEndian.PutUint32(b[start:], checksum(b[start+4:]))
	return b
}

// frameSize is the bytes an item takes on disk.
func frameSize(payload []byte) int64 { return frameHeaderSize + int64(len(payload)) }

// readPosition reads the position file, and says whether it could.
func readPosition(root *os.Root) (Position, bool) {
	data, err := root.ReadFile(positionFile)
	if err != nil || len(data) != positionSize {
		return Position{}, false
	}
	if checksum(data[:positionSize-4]) != binary.LittleEndian.Uint32(data[positionSize-4:]) {
		return Position{}, false
	}
	offset, fit := fits.Int64(binary.LittleEndian.Uint64(data[4:]))
	return Position{
		number: binary.LittleEndian.Uint32(data),
		offset: offset,
		item:   binary.LittleEndian.Uint64(data[12:]),
	}, fit
}

// writePosition moves the sender to at, and writes it in place. The
// writer syncs it.
func (d *Folder) writePosition(at Position) {
	d.mu.Lock()
	d.at = at
	d.mu.Unlock()
	var data [positionSize]byte
	binary.LittleEndian.PutUint32(data[:], at.number)
	// An offset is never negative.
	offset, _ := fits.Uint64(int(at.offset))
	binary.LittleEndian.PutUint64(data[4:], offset)
	binary.LittleEndian.PutUint64(data[12:], at.item)
	binary.LittleEndian.PutUint32(data[20:], checksum(data[:20]))
	// A Position that was not written makes the sender start further
	// back, and send some items twice, which is allowed.
	_, _ = d.pos.WriteAt(data[:], 0)
	d.posDirty.Store(true)
}

// startFile starts queue file number, whose first item is first, and
// syncs it, so it is on disk before anything is written to it.
func (d *Folder) startFile(number uint32, first uint64) error {
	w, err := createQueueFile(d.root, queueFileName(number), os.O_EXCL, first, nil)
	if err != nil {
		return err
	}
	d.budget.TakeAnyway(queueHeaderSize)
	d.w = w
	d.mu.Lock()
	d.files = append(d.files, queueFile{number: number, first: first, size: queueHeaderSize})
	d.mu.Unlock()
	return nil
}

// createQueueFile makes a queue file whose first item is first, open to
// the proxy's user only, writes its header and frames, and syncs it.
func createQueueFile(root *os.Root, name string, flag int, first uint64, frames []byte) (*os.File, error) {
	w, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|flag, 0o600)
	if err != nil {
		return nil, err
	}
	header := binary.LittleEndian.AppendUint64(append([]byte(queueMagic), queueVersion), first)
	_, err = w.Write(append(header, frames...))
	if err = errors.Join(err, w.Sync()); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// Write appends items to the newest queue file, starting the next one when
// it is full, and says how many it wrote. The items' bytes were reserved
// in the budget when they were queued. A write that fails starts the next
// file, so the next write has a whole file to go to. A record with more
// than one part is refused before anything is written. Only the writer
// calls it.
func (d *Folder) Write(queued []Item) (int, error) {
	items, err := frames(queued)
	if err != nil {
		return 0, err
	}
	var buf []byte
	written := 0
	for len(items) > 0 {
		d.mu.Lock()
		last := d.files[len(d.files)-1]
		d.mu.Unlock()
		room := queueFileSize - last.size
		buf = buf[:0]
		n := 0
		for _, item := range items {
			size := frameSize(item.payload)
			// An item larger than a whole file still goes in a file of
			// its own.
			if int64(len(buf))+size > room && (len(buf) > 0 || last.size > queueHeaderSize) {
				break
			}
			buf = appendFrame(buf, item)
			n++
		}
		if n == 0 {
			if err := d.nextFile(); err != nil {
				return written, err
			}
			continue
		}
		if _, err := d.w.Write(buf); err != nil {
			return written, errors.Join(err, d.nextFile())
		}
		d.dirty = true
		d.mu.Lock()
		d.files[len(d.files)-1].size += int64(len(buf))
		d.next += uint64(n)
		d.mu.Unlock()
		items = items[n:]
		written += n
	}
	return written, nil
}

// nextFile syncs and closes the newest queue file, and starts the next.
// A file that could not be synced or closed is still left for the next,
// so one broken file does not stop every later write.
func (d *Folder) nextFile() error {
	err := errors.Join(d.w.Sync(), d.w.Close())
	d.dirty = false
	d.mu.Lock()
	last := d.files[len(d.files)-1]
	next := d.next
	d.mu.Unlock()
	return errors.Join(err, d.startFile(last.number+1, next))
}

// Sync syncs the newest queue file and the position file to disk, when
// anything was written since the last sync. Only the writer calls it.
func (d *Folder) Sync() error {
	var errs []error
	if d.dirty {
		errs = append(errs, d.w.Sync())
		d.dirty = false
	}
	if d.posDirty.Swap(false) {
		errs = append(errs, d.pos.Sync())
	}
	return errors.Join(errs...)
}

// Read reads up to limit records or spans from where the sender is, and
// gives the Position after them. What it cannot read, an item or the rest
// of a file, is skipped to the next file and counted in lost; in the file
// being written that cannot happen, as only whole items are written there.
// Only the sender calls it.
func (d *Folder) Read(limit int) ([]Item, Position, int) {
	d.mu.Lock()
	files := slices.Clone(d.files)
	d.mu.Unlock()
	at, lost := d.at, 0
	// reached is the next item the sender has not had. A file put in front
	// at a stop is followed by the file the sender was in, whose items
	// before reached were already sent.
	reached := at.item
	var items []Item
	var oldest time.Time
	count := 0
	// The sender's file is always one of the files: only those before it
	// are ever deleted.
	i := slices.IndexFunc(files, func(f queueFile) bool { return f.number == at.number })
	for count < limit {
		f := files[i]
		data, ok := d.readAt(f, at.offset)
		for ok && len(data) > 0 && count < limit {
			got, n, err := readFrame(data)
			if err != nil {
				ok = false
				break
			}
			data = data[n:]
			at.offset += n
			at.item++
			if at.item <= reached {
				continue
			}
			if len(items) == 0 {
				oldest = got.queued
			}
			item := Item{Kind: got.kind, Parts: [][]byte{slices.Clone(got.payload)}, Queued: got.queued}
			if got.kind == Spans {
				item.Parts = splitSpans(got.payload)
			}
			items = append(items, item)
			count += len(item.Parts)
		}
		if i == len(files)-1 {
			if !ok {
				// Only a file changed under the proxy gets here: what is
				// left of it is skipped.
				lost++
				at.offset = f.size
			}
			break
		}
		if ok && at.offset < f.size {
			break
		}
		if !ok {
			// Item numbers only grow, so the gap fits; a file that says
			// otherwise counts as one lost item.
			gap, _ := fits.Int(files[i+1].first - at.item)
			lost += max(1, gap)
		}
		// A file read to its end is passed, so taken can delete it.
		reached = max(reached, at.item)
		i++
		at = Position{number: files[i].number, offset: queueHeaderSize, item: files[i].first}
	}
	d.mu.Lock()
	d.lastReadQueued = oldest
	d.mu.Unlock()
	return items, at, lost
}

// readAt reads queue file f from offset to its end as written, through
// the sender's own handle, and says whether it could.
func (d *Folder) readAt(f queueFile, offset int64) ([]byte, bool) {
	if d.r == nil || d.rNumber != f.number {
		if d.r != nil {
			_ = d.r.Close()
		}
		r, err := d.root.Open(queueFileName(f.number))
		if err != nil {
			d.r = nil
			return nil, false
		}
		d.r, d.rNumber = r, f.number
	}
	data := make([]byte, f.size-offset)
	_, err := d.r.ReadAt(data, offset)
	return data, err == nil
}

// Taken moves the sender to at, past items the collector took or refused
// for good, and deletes the queue files it is past. Only the sender calls
// it.
func (d *Folder) Taken(at Position) {
	d.writePosition(at)
	d.mu.Lock()
	var gone []queueFile
	for len(d.files) > 1 && d.files[0].number < at.number {
		gone = append(gone, d.files[0])
		d.files = d.files[1:]
	}
	if d.emptyLocked() {
		d.lastReadQueued = time.Time{}
	}
	d.mu.Unlock()
	for _, f := range gone {
		if d.r != nil && d.rNumber == f.number {
			_ = d.r.Close()
			d.r = nil
		}
		// A file not deleted now is deleted at the next start.
		_ = d.root.Remove(queueFileName(f.number))
		d.budget.GiveBack(f.size)
	}
}

// Empty says the sender has read and handed on everything written.
func (d *Folder) Empty() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.emptyLocked()
}

// emptyLocked is empty, for a caller that holds mu.
func (d *Folder) emptyLocked() bool {
	last := d.files[len(d.files)-1]
	return d.at.number == last.number && d.at.offset >= last.size
}

// FileBytes is the bytes the queue files take, what was sent included
// until a whole file is.
func (d *Folder) FileBytes() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	size := int64(0)
	for _, f := range d.files {
		size += f.size
	}
	return size
}

// OldestQueued is when the sender's next item was queued: zero when it
// has not read it yet, or nothing waits.
func (d *Folder) OldestQueued() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastReadQueued
}

// WriteFront puts items before everything else on disk, in a new queue
// file numbered before the oldest one, and moves the sender there. A stop
// uses it for the items still in memory, which are older than those on
// disk. A record with more than one part is refused before anything is
// written. The writer has stopped.
func (d *Folder) WriteFront(front []Item) error {
	items, err := frames(front)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	var buf []byte
	for _, item := range items {
		buf = appendFrame(buf, item)
	}
	number, first := d.at.number-1, d.at.item-uint64(len(items))
	w, err := createQueueFile(d.root, queueFileName(number), os.O_TRUNC, first, buf)
	if err != nil {
		return err
	}
	// The file is written and synced, so closing it loses nothing.
	_ = w.Close()
	size := int64(queueHeaderSize + len(buf))
	d.budget.TakeAnyway(size)
	d.mu.Lock()
	d.files = append([]queueFile{{number: number, first: first, size: size}}, d.files...)
	d.mu.Unlock()
	d.writePosition(Position{number: number, offset: queueHeaderSize, item: first})
	return nil
}

// Close syncs what was written and closes the files.
func (d *Folder) Close() error {
	d.posDirty.Store(true)
	d.dirty = true
	err := d.Sync()
	err = errors.Join(err, d.w.Close(), d.pos.Close(), d.root.Close())
	if d.r != nil {
		err = errors.Join(err, d.r.Close())
	}
	return err
}

// joinSpans is one call's spans as one payload, each with its length
// first, so they are written and read together.
func joinSpans(spans [][]byte) []byte {
	n := 0
	for _, s := range spans {
		n += binary.MaxVarintLen64 + len(s)
	}
	b := make([]byte, 0, n)
	for _, s := range spans {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}

// splitSpans reads back the spans joinSpans put in one payload. The crc
// has checked the bytes, so a length that does not fit ends the list.
func splitSpans(payload []byte) [][]byte {
	var spans [][]byte
	for len(payload) > 0 {
		length, read := binary.Uvarint(payload)
		n, fit := fits.Int(length)
		if read <= 0 || !fit || len(payload)-read < n {
			break
		}
		spans = append(spans, slices.Clone(payload[read:read+n]))
		payload = payload[read+n:]
	}
	return spans
}

// Path is the folder the queue files are in.
func (d *Folder) Path() string { return d.dir }
