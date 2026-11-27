Feature: The disk queue
  An OTLP exporter that names a queueFolder keeps on disk what no longer
  fits in its memory queue, up to queueDisk bytes. In normal traffic only
  memory is used. Once anything is on disk, new records go to disk too
  until it is empty, so the order is kept; the sender sends memory first,
  then the disk. An item leaves the disk only once the collector took it.
  A call never waits for the disk (Peter, 2026-11-27). Requirements:
  otel-disk-buffer/requirements.md in the project files.

  Background:
    Given a fake OTLP receiver

  Scenario: Normal traffic does not touch the disk
    Given an exporter with queueSize 100 and a disk queue
    When 5 calls end one after another
    Then the collector gets 5 records
    And the disk queue holds no record

  Scenario: A full memory queue goes on to the disk, in order
    Given an exporter with queueSize 3 and a disk queue, whose collector is busy
    When 10 calls end one after another
    And the collector is back, and 1 more call ends at once
    Then the collector gets records 1 to 11 in the order of the calls
    And every call's spans
    And the proxy logs no warning about dropped records
    And the proxy warns that the export queue was full and new items go to the disk queue
    And no call was blocked or slowed

  Scenario: A clean stop writes what waits in memory to disk
    Given an exporter with queueSize 100 and a disk queue, whose collector is busy
    When 10 calls end one after another
    And the proxy stops, and starts again with the collector back
    Then the collector gets records 1 to 10 in the order of the calls

  Scenario: An item that cannot be read is skipped, counted and warned about
    The last item on disk is the batch record the stop made, after the
    three records.
    Given an exporter with queueSize 1 and a disk queue, whose collector is busy
    When 3 calls end one after another
    And the proxy stops cleanly
    And the last item on disk is damaged
    And the proxy starts again with the collector back
    Then the collector gets records 1 to 3 in the order of the calls
    And the proxy warns that 1 item on disk could not be read

  Scenario: The disk queue's files are the proxy's own
    Given an exporter with queueSize 1 and a disk queue, whose collector is busy
    When 3 calls end one after another
    Then the queue folder and its files are open to the proxy's user only
