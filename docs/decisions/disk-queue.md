# Disk queue

Decided with Peter on 2026-11-27 (Q1 to Q10 in `otel-disk-buffer/findings.md` in the project files, which also hold the requirements and Vector's design it follows).

- **A call never waits** (must): not for the collector, the disk or a sync. A call only hands its record over; the exporter's own writer writes and syncs, and its sender reads (Q1).
- **A busy collector never loses a batch.** An item leaves the queue only on a 2xx reply, or a refusal for good (another 4xx), counted as `send_failed`. A busy or unreachable collector (429, 502, 503, 504, no connection) keeps the batch first in line, tried again after 1 second, growing to 5. A full queue still refuses the newest, as `queue_full` (Q2).
- **On when a folder is named.** An OTLP exporter with `queueFolder` and `queueDisk` (no default, at least 64 MiB; one without the other is a config error) keeps on disk what no longer fits in memory. In normal traffic the disk is not touched. Once anything is on disk, every new item goes there too until it is empty, so order is kept (Q3, Q4, Q8).
- **One sender, in order:** memory first, then the disk, oldest first (Q10).
- **Sync at most every 500 ms**, fixed, as Vector does: a power cut loses at most about that much; a crash loses what was still in memory (Q5).
- **Written to disk on stop:** half the stop time goes to sending, then what is still in memory goes to a queue file before everything on disk, so the next start sends it first (Q6).
- **Batch records in the same queue,** right after the records they cover, with 64 places kept for them, so a full queue never refuses one. The separate batch line is gone (Q7).
- **At least once:** a batch the collector took but whose reply was lost is sent again. `hotline-proxy verify` shows an exact copy as the note `records_repeated` or `batch_record_repeated`, not a problem; a copy that differs in anything stays `record_extra` or `chain_broken` (Q9).
- **Files:** `<queueFolder>/hotline.lock`, then `records/` and `spans/`, each with numbered queue files of at most 16 MiB and a `position` file. A queue file starts with `HTLQ`, a version byte (1) and the number of its first item. Each item is its length, a CRC32C of the rest, its kind (a record, a batch record, or one call's spans), the time it was first queued (Unix nanoseconds, so the oldest item's age can be told after a restart) and its bytes. Folder 0700, files 0600. A broken item is skipped, counted as `disk_unreadable` and warned about.
- **Numbers:** `hotline.exporter.queue.disk.usage` (bytes the disk queue's files take) and `hotline.exporter.queue.oldest.age` (seconds the oldest waiting item has waited, 0 when none). Warnings at most once a minute: the disk started being used, the disk queue is full, items could not be read.
- **Load test:** every `make load` line gives the exporter a disk queue, and one line stops the fake receiver for 20 seconds in the middle of each repeat (`-outage`), reporting what was lost, the most on disk, the longest wait and the time to send it all.
- Changing `queueFolder` or `queueDisk` needs a restart, as any exporter change does.

## Tripwires (2026-11-27)

- Confirmation: with a busy collector and a memory queue of 3, 11 calls reach the collector in order once it is back, none is dropped, and no call was slowed.
- Revisit if outages often outlast `queueDisk`, or a crash losing the memory part matters in normal traffic (Q3).
- Revisit if people care more about the latest records than the earliest after long outages (Q2).
- Revisit if the load test shows one sender can't keep up with traffic plus the waiting disk (Q10).
