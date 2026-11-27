package export

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The disk queue is the part of an OTLP exporter's queue kept as files in
// its queueFolder, used once the memory queue is full and until it is
// empty again (Peter, 2026-11-27). Each signal has its own subfolder:
// numbered queue files written one after another, and a position file
// saying where the sender is. The design follows Vector's disk buffer:
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
	// frameHeaderSize is an item's length, crc and kind, before its
	// payload.
	frameHeaderSize = 4 + 4 + 1
	// positionSize is the position file: the queue file's number, the
	// offset and number of the next item there, and a crc of the three.
	positionSize = 4 + 8 + 8 + 4
	// syncEvery is how often what was written is synced to disk at most,
	// fixed, as in Vector: a power cut loses at most about this much
	// (Peter, 2026-11-27).
	syncEvery = 500 * time.Millisecond
	// The numbers start high, so a stop can put the items still in
	// memory in a file before the oldest one, in order.
	firstFileNumber = 1 << 20
	firstItemNumber = 1 << 40
	// queueLockFile is held while a proxy uses the folder.
	queueLockFile = "hotline.lock"
	positionFile  = "position"
)

// itemKind says what an item on disk holds.
type itemKind byte

const (
	kindRecord itemKind = 1
	kindBatch  itemKind = 2
	// kindSpans is one call's spans, kept together.
	kindSpans itemKind = 3
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// errQueueFile is an item on disk that cannot be read: a wrong crc, or
// cut short.
var errQueueFile = errors.New("export: a queue file item cannot be read")

// diskBudget is the bytes one exporter's disk queues may hold in all,
// records and spans together: what waits to be written and what the
// queue files hold.
type diskBudget struct {
	limit int64
	used  atomic.Int64
}

// take reserves n bytes, and says whether they fit.
func (b *diskBudget) take(n int64) bool {
	if b.used.Add(n) > b.limit {
		b.used.Add(-n)
		return false
	}
	return true
}

// queueFile is one queue file: its number, the number of its first item,
// and its size in bytes, all of it written.
type queueFile struct {
	number uint32
	first  uint64
	size   int64
}

// position is where the sender is: a queue file, the offset of its next
// item there and that item's number.
type position struct {
	number uint32
	offset int64
	item   uint64
}

// spilled is one item waiting to be written to disk.
type spilled struct {
	kind    itemKind
	payload []byte
}

// diskItem is one item read from disk: a record, a batch record, or one
// call's spans.
type diskItem struct {
	kind  itemKind
	items [][]byte
}

// diskQueue is the queue files of one signal. One writer appends and syncs;
// the sender reads, and moves its position once the collector took what
// it read. mu guards files and next; each side owns its own file handles.
type diskQueue struct {
	dir    string
	budget *diskBudget

	mu sync.Mutex
	// files are the queue files, oldest first; the last is being written.
	files []queueFile
	// next is the number of the next item written.
	next uint64

	// The writer's side.
	w     *os.File
	dirty bool

	// The sender's side.
	at       position
	r        *os.File
	rNumber  uint32
	pos      *os.File
	posDirty atomic.Bool
}

// queueFileName is a queue file's name: its number, so a plain sort gives
// the oldest first.
func queueFileName(number uint32) string { return fmt.Sprintf("%08d.queue", number) }

// openDiskQueue opens the queue files in dir, or starts them. It reads the
// position file; when that cannot be read the sender starts at the oldest
// file, and the collector may get some items twice. A cut-off item at the
// end of the newest file, from a crash while it was written, is cut away
// and counted in lost, as is a file with no readable header. New items
// always go to a new file.
func openDiskQueue(dir string, budget *diskBudget) (*diskQueue, int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, 0, err
	}
	d := &diskQueue{dir: dir, budget: budget}
	at, known := readPosition(filepath.Join(dir, positionFile))
	lost := 0
	for _, number := range listQueueFiles(dir) {
		f, err := statQueueFile(dir, number)
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
		_ = os.Remove(filepath.Join(dir, queueFileName(number)))
	}
	number, first := uint32(firstFileNumber), uint64(firstItemNumber)
	d.next = first
	if n := len(d.files); n > 0 {
		lost += d.cutTail()
		number, first = d.files[n-1].number+1, d.next
	}
	if err := d.startFile(number, first); err != nil {
		return nil, 0, err
	}
	if !known || !slices.ContainsFunc(d.files, func(f queueFile) bool { return f.number == at.number }) {
		at = position{number: d.files[0].number, offset: queueHeaderSize, item: d.files[0].first}
	}
	d.at = at
	// The folder could be written to just now, so the position file can
	// be made.
	d.pos, _ = os.OpenFile(filepath.Join(dir, positionFile), os.O_CREATE|os.O_RDWR, 0o600)
	d.writePosition()
	return d, lost, nil
}

// cutTail finds where the newest queue file's last whole item ends, and
// cuts away anything after it: an item a crash cut short. It counts that
// item as lost, and sets the number of the next item.
func (d *diskQueue) cutTail() int {
	last := &d.files[len(d.files)-1]
	path := filepath.Join(d.dir, queueFileName(last.number))
	// statQueueFile has just read the file.
	data, _ := os.ReadFile(path)
	offset, count := int64(queueHeaderSize), uint64(0)
	for {
		_, _, n, err := readFrame(data[offset:])
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
	_ = os.Truncate(path, offset)
	d.budget.used.Add(offset - last.size)
	last.size = offset
	return 1
}

// listQueueFiles are the numbers of the queue files in dir, oldest first.
// openDiskQueue has made dir, so it can be read.
func listQueueFiles(dir string) []uint32 {
	entries, _ := os.ReadDir(dir)
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
func statQueueFile(dir string, number uint32) (queueFile, error) {
	f, err := os.Open(filepath.Join(dir, queueFileName(number)))
	if err != nil {
		return queueFile{}, err
	}
	defer f.Close()
	var header [queueHeaderSize]byte
	_, err = io.ReadFull(f, header[:])
	if err != nil || string(header[:4]) != queueMagic || header[4] != queueVersion {
		return queueFile{}, errQueueFile
	}
	// The file was read, so it has a size.
	size, _ := f.Seek(0, io.SeekEnd)
	return queueFile{number: number, first: binary.LittleEndian.Uint64(header[5:]), size: size}, nil
}

// readFrame reads one item at the start of data: its kind, its payload and
// how many bytes it takes.
func readFrame(data []byte) (itemKind, []byte, int64, error) {
	if len(data) < frameHeaderSize {
		return 0, nil, 0, errQueueFile
	}
	length := int64(binary.LittleEndian.Uint32(data))
	if int64(len(data)) < frameHeaderSize+length {
		return 0, nil, 0, errQueueFile
	}
	body := data[8 : frameHeaderSize+length]
	if crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(data[4:]) {
		return 0, nil, 0, errQueueFile
	}
	return itemKind(body[0]), body[1:], frameHeaderSize + length, nil
}

// appendFrame writes one item: length, crc, kind and payload.
func appendFrame(b []byte, kind itemKind, payload []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
	start := len(b)
	b = append(b, 0, 0, 0, 0, byte(kind))
	b = append(b, payload...)
	binary.LittleEndian.PutUint32(b[start:], crc32.Checksum(b[start+4:], crcTable))
	return b
}

// frameSize is the bytes an item takes on disk.
func frameSize(payload []byte) int64 { return frameHeaderSize + int64(len(payload)) }

// readPosition reads the position file, and says whether it could.
func readPosition(path string) (position, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) != positionSize {
		return position{}, false
	}
	if crc32.Checksum(data[:positionSize-4], crcTable) != binary.LittleEndian.Uint32(data[positionSize-4:]) {
		return position{}, false
	}
	return position{
		number: binary.LittleEndian.Uint32(data),
		offset: int64(binary.LittleEndian.Uint64(data[4:])),
		item:   binary.LittleEndian.Uint64(data[12:]),
	}, true
}

// writePosition writes where the sender is, in place. The writer syncs it.
func (d *diskQueue) writePosition() {
	var data [positionSize]byte
	binary.LittleEndian.PutUint32(data[:], d.at.number)
	binary.LittleEndian.PutUint64(data[4:], uint64(d.at.offset))
	binary.LittleEndian.PutUint64(data[12:], d.at.item)
	binary.LittleEndian.PutUint32(data[20:], crc32.Checksum(data[:20], crcTable))
	// A position that was not written makes the sender start further
	// back, and send some items twice, which is allowed.
	_, _ = d.pos.WriteAt(data[:], 0)
	d.posDirty.Store(true)
}

// startFile starts queue file number, whose first item is first, and
// syncs it, so it is on disk before anything is written to it.
func (d *diskQueue) startFile(number uint32, first uint64) error {
	w, err := createQueueFile(filepath.Join(d.dir, queueFileName(number)), os.O_EXCL, first, nil)
	if err != nil {
		return err
	}
	d.budget.used.Add(queueHeaderSize)
	d.w = w
	d.mu.Lock()
	d.files = append(d.files, queueFile{number: number, first: first, size: queueHeaderSize})
	d.mu.Unlock()
	return nil
}

// createQueueFile makes a queue file whose first item is first, open to
// the proxy's user only, writes its header and frames, and syncs it.
func createQueueFile(path string, flag int, first uint64, frames []byte) (*os.File, error) {
	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|flag, 0o600)
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

// write appends items to the newest queue file, starting the next one when
// it is full. The items' bytes were reserved in the budget when they were
// queued. Only the writer calls it.
func (d *diskQueue) write(items []spilled) error {
	var buf []byte
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
			buf = appendFrame(buf, item.kind, item.payload)
			n++
		}
		if n == 0 {
			if err := d.nextFile(); err != nil {
				return err
			}
			continue
		}
		if _, err := d.w.Write(buf); err != nil {
			return err
		}
		d.dirty = true
		d.mu.Lock()
		d.files[len(d.files)-1].size += int64(len(buf))
		d.next += uint64(n)
		d.mu.Unlock()
		items = items[n:]
	}
	return nil
}

// nextFile syncs and closes the newest queue file, and starts the next.
func (d *diskQueue) nextFile() error {
	if err := errors.Join(d.w.Sync(), d.w.Close()); err != nil {
		return err
	}
	d.dirty = false
	d.mu.Lock()
	last := d.files[len(d.files)-1]
	next := d.next
	d.mu.Unlock()
	return d.startFile(last.number+1, next)
}

// sync syncs the newest queue file and the position file to disk, when
// anything was written since the last sync. Only the writer calls it.
func (d *diskQueue) sync() error {
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

// read reads up to limit records or spans from where the sender is, and
// gives the position after them. What it cannot read, an item or the rest
// of a file, is skipped to the next file and counted in lost; in the file
// being written that cannot happen, as only whole items are written there.
// Only the sender calls it.
func (d *diskQueue) read(limit int) ([]diskItem, position, int) {
	d.mu.Lock()
	files := slices.Clone(d.files)
	d.mu.Unlock()
	at, lost := d.at, 0
	var items []diskItem
	count := 0
	// The sender's file is always one of the files: only those before it
	// are ever deleted.
	i := slices.IndexFunc(files, func(f queueFile) bool { return f.number == at.number })
	for count < limit {
		f := files[i]
		data, ok := d.readAt(f, at.offset)
		for ok && len(data) > 0 && count < limit {
			kind, payload, n, err := readFrame(data)
			if err != nil {
				ok = false
				break
			}
			item := diskItem{kind: kind, items: [][]byte{slices.Clone(payload)}}
			if kind == kindSpans {
				item.items = splitSpans(payload)
			}
			items = append(items, item)
			count += len(item.items)
			data = data[n:]
			at.offset += n
			at.item++
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
			lost += int(max(1, files[i+1].first-at.item))
		}
		// A file read to its end is passed, so taken can delete it.
		i++
		at = position{number: files[i].number, offset: queueHeaderSize, item: files[i].first}
	}
	return items, at, lost
}

// readAt reads queue file f from offset to its end as written, through
// the sender's own handle, and says whether it could.
func (d *diskQueue) readAt(f queueFile, offset int64) ([]byte, bool) {
	if d.r == nil || d.rNumber != f.number {
		if d.r != nil {
			_ = d.r.Close()
		}
		r, err := os.Open(filepath.Join(d.dir, queueFileName(f.number)))
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

// taken moves the sender to at, past items the collector took or refused
// for good, and deletes the queue files it is past. Only the sender calls
// it.
func (d *diskQueue) taken(at position) {
	d.at = at
	d.writePosition()
	d.mu.Lock()
	var gone []queueFile
	for len(d.files) > 1 && d.files[0].number < at.number {
		gone = append(gone, d.files[0])
		d.files = d.files[1:]
	}
	d.mu.Unlock()
	for _, f := range gone {
		if d.r != nil && d.rNumber == f.number {
			_ = d.r.Close()
			d.r = nil
		}
		// A file not deleted now is deleted at the next start.
		_ = os.Remove(filepath.Join(d.dir, queueFileName(f.number)))
		d.budget.used.Add(-f.size)
	}
}

// empty says the sender has read and handed on everything written.
func (d *diskQueue) empty() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	last := d.files[len(d.files)-1]
	return d.at.number == last.number && d.at.offset >= last.size
}

// writeFront puts items before everything else on disk, in a new queue
// file numbered before the oldest one, and moves the sender there. A stop
// uses it for the items still in memory, which are older than those on
// disk. The writer has stopped.
func (d *diskQueue) writeFront(items []spilled) error {
	if len(items) == 0 {
		return nil
	}
	var buf []byte
	for _, item := range items {
		buf = appendFrame(buf, item.kind, item.payload)
	}
	number, first := d.at.number-1, d.at.item-uint64(len(items))
	w, err := createQueueFile(filepath.Join(d.dir, queueFileName(number)), os.O_TRUNC, first, buf)
	if err != nil {
		return err
	}
	// The file is written and synced, so closing it loses nothing.
	_ = w.Close()
	size := int64(queueHeaderSize + len(buf))
	d.budget.used.Add(size)
	d.mu.Lock()
	d.files = append([]queueFile{{number: number, first: first, size: size}}, d.files...)
	d.mu.Unlock()
	d.at = position{number: number, offset: queueHeaderSize, item: first}
	d.writePosition()
	return nil
}

// close syncs what was written and closes the files.
func (d *diskQueue) close() error {
	d.posDirty.Store(true)
	d.dirty = true
	err := d.sync()
	err = errors.Join(err, d.w.Close(), d.pos.Close())
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
		n, read := binary.Uvarint(payload)
		if read <= 0 || uint64(len(payload)-read) < n {
			break
		}
		spans = append(spans, slices.Clone(payload[read:read+int(n)]))
		payload = payload[read+int(n):]
	}
	return spans
}
