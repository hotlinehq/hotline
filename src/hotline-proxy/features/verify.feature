# Agreed with Peter, 2026-11-16. Detail: docs/proxy-ledger.md.
# Proxy part only: no minute entry, no time stamp, no witness (those come with the control plane).
Feature: Checking records with hotline-proxy verify
  hotline-proxy verify reads records as the file exporter writes them, one
  JSON line per call, with the batch records among them. It needs no config,
  no key and no network: everything it checks is in the lines. It checks
  that each record's hash rebuilds from the record, that each batch's root
  rebuilds from its records and is signed by the key the batch names, and
  that each run's batches form an unbroken chain. The file exporter keeps a
  call's spans inside its line, so verify also rebuilds each call's
  hotline.trace.hash from its spans.

  It prints one line per run and per problem, in plain words. With
  --format json, which every command takes, it prints one JSON object on
  standard output instead, for scripts and other tools. It exits with 0 when
  everything checks out, 1 when it found a problem, and 2 when it could not
  read its input, in both formats.

  Background:
    Given the proxy's clock is a test clock, so a wait of minutes passes at once
    And a file exporter writing to the folder calls

  Rule: Good records check out

    Scenario: A decimal body field keeps its point in the file
      A whole number and a decimal hash differently, so the file writes a
      decimal such as 2.0 with its point, and verify reads it back as one.
      Given the overlay weight.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Weight, version: "1" }
        actions:
          - target: $.components.schemas.Order.properties
            update:
              weight: { type: [number, "null"], x-hotline-record: shop.order.weight }
        """
      And the checkout integration's shop spec has the overlay weight.overlay.yaml
      And the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2,"weight":2.0}
      And the app sends POST /orders with "Idempotency-Key: k2" and the json body {"item":"A1","quantity":2,"weight":1.5}
      And I run hotline-proxy verify calls
      Then it exits with 0
      And the file writes shop.order.weight as 2.0 and as 1.5

    Scenario: A whole folder checks out
      When the app sends GET /orders 5 times
      And 30 seconds pass
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0
      And it prints that checkout's 5 records and their traces in 2 batches check out, and the run ended with a clean stop

    Scenario: Rotated and compressed files are read too
      Given the file exporter starts a new file past 1 KiB and compresses the old ones
      When the app sends GET /orders 50 times
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0
      And it read every file in the folder, in order

    Scenario: Verify needs no config, no key and no network
      Given the folder calls from a stopped proxy
      When I run hotline-proxy verify calls with no config and no network
      Then it exits with 0

    Scenario: Several runs in one folder are checked one by one
      When the app sends GET /orders
      And hotline-proxy is stopped and started again
      And the app sends GET /orders
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0
      And it prints 2 runs, each with its own instance, from sequence 0, ending with a clean stop

    Scenario: One integration is checked on its own
      Given the integrations checkout and refunds
      When the app sends GET /orders as checkout 3 times and GET /refunds as refunds 2 times
      And hotline-proxy is stopped
      And only the checkout records and batch records are kept in the folder
      And I run hotline-proxy verify calls --integration checkout
      Then it exits with 0
      And it prints that checkout's 3 records check out, and mentions no other integration

    Scenario: A run with a named instance shows its run id
      Given the config has ledger instance { env: POD_NAME } and POD_NAME is checkout-0
      And the folder calls from a stopped proxy, with 1 calls
      When I run hotline-proxy verify calls
      Then it exits with 0
      And it prints the instance checkout-0 with the run id proxy-test-1

    Scenario: Each integration's chain is reported on its own
      Given the integrations checkout and refunds
      When the app sends GET /orders as checkout and GET /refunds as refunds
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it prints one line for checkout and one for refunds, each with its own batches

  Rule: A change shows, and verify says where

    Scenario: A changed record
      Given the folder calls from a stopped proxy, with 3 calls
      When http.response.status_code is changed in the second record
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the record with that span id does not match its hotline.record.hash

    Scenario: A changed record with its hash recomputed
      Given the folder calls from a stopped proxy, with 3 calls
      When http.response.status_code is changed and hotline.record.hash recomputed in the second record
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that batch 0 of that run does not rebuild its root from its records

    Scenario: A record deleted from a batch
      Given the folder calls from a stopped proxy, with 3 calls
      When the second record is deleted
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that batch 0 has 2 of 3 records and index 1 is missing

    Scenario: A record added to a batch
      Given the folder calls from a stopped proxy, with 3 calls
      When a copy of the second record with a new span id and recomputed hash is added
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that batch 0 holds a record it does not count

    Scenario: A changed span in a call's trace
      Given the folder calls from a stopped proxy, with 3 calls
      When the end of a span is changed in the second line's spans
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the trace of the record with that span id does not match its hotline.trace.hash

    Scenario Outline: A span id written another way than the proxy writes it
      Given the folder calls from a stopped proxy, with 3 calls
      When the <field> of the last span in the second line's spans is changed to <id>
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the trace of the record with that span id does not match its hotline.trace.hash

      Examples:
        | field          | id                                  |
        | span_id        | the same id in capital letters      |
        | parent_span_id | zeros, which stand for no span      |

    Scenario: A span removed from a call's trace
      Given the folder calls from a stopped proxy, with 3 calls
      When one span is removed from the second line's spans
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the trace of that record does not match its hotline.trace.hash

    Scenario: A changed batch record
      Given the folder calls from a stopped proxy
      When hotline.batch.records is changed in the first batch record
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the signature of batch 0 does not check out with its key

    Scenario: A deleted batch record
      Given the folder calls from a stopped proxy, with calls in 3 batch windows
      When the batch record with sequence 1 is deleted
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the run's chain is broken: batch 1 is missing
      And it prints that the records naming batch 1 have no batch record

    Scenario: A batch put back in the wrong place
      Given the folder calls from a stopped proxy, with calls in 3 batch windows
      When batch 2's hotline.batch.previous is changed to the root of batch 0
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the chain is broken between batch 1 and batch 2

    Scenario: A run cut short
      Given the folder calls from a proxy that was killed
      When I run hotline-proxy verify calls
      Then it exits with 0
      And it prints that the run has no last batch: it crashed, or its end is missing

  Rule: Dropped records are explained, not counted as changes

    Scenario: A dropped record listed by the exporter
      Given the file exporter's queue was full for one record of batch 0
      When I run hotline-proxy verify calls
      Then it exits with 0
      And it prints that batch 0 rebuilds from 2 records and 1 dropped hash
      And it prints the index of the dropped record

    Scenario: Too many dropped records to list
      Given the file exporter's queue was full for 1,025 records of batch 0
      When I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that batch 0 cannot be proven from this copy: 1,025 records were dropped by this exporter

    Scenario: Batch records lost while the collector was away
      Given a batch record in calls with hotline.batch.lost_batches 2
      When I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that 2 batches before it were lost by this exporter, and their records cannot be proven

  Rule: Bodies are checked against a record

    Scenario: A body the customer kept matches its record
      Given the vendor answered GET /orders with the json body {"orders":[]}
      And the body was saved as reply.json
      When I run hotline-proxy verify calls --body reply.json --span 00f067aa0ba902b7 --side response
      Then it exits with 0
      And it prints that the response body matches the record's hotline.response.body.hash

    Scenario: A different body does not match
      Given the vendor answered GET /orders with the json body {"orders":[]}
      When I run hotline-proxy verify calls --body other.json --span 00f067aa0ba902b7 --side response
      Then it exits with 1
      And it prints that the response body does not match

    Scenario Outline: A body check that cannot be made
      Given the vendor answered GET /orders with the json body {"orders":[]}
      When I run hotline-proxy verify calls --body <body> --span <span> --side response
      Then it exits with 1
      And it prints <message>

      Examples:
        | body      | span             | message                           |
        | calls     | 00f067aa0ba902b7 | that the body cannot be read      |
        | reply.json | 0000000000000000 | that no record has that span id  |

    Scenario: A body kept whole in a full record is checked by itself
      Given the integration has content full and maxBodySize 1MiB
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it prints that 1 kept body matches its hash

    Scenario: A body cut short by maxBodySize is not checked
      Given the integration has content full and maxBodySize 1KiB
      When the app sends POST /orders with a json body of 3 KiB
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0
      And it prints that 1 kept body was cut short and could not be checked

    Scenario: A full record with its login hidden checks out
      The content hash covers the content as written, with REDACTED in it
      (Peter, 2026-11-18).
      Given the integration has content full and maxBodySize 1MiB
      When the app sends GET /orders
      And hotline-proxy is stopped
      And I run hotline-proxy verify calls
      Then it exits with 0
      And the line has hotline.content.request.header.authorization REDACTED

  Rule: With --format json, the result is one JSON object

    The object holds a summary, one entry per run, and one entry per
    problem. Each problem has a fixed code for scripts and a message in
    plain words. Values taken from the input are escaped inside the JSON
    strings, as in the console exporter.

      {
        "format": "hotline.verify.v1",
        "ok": false,
        "summary": { "records": 5, "batches": 2, "runs": 1, "problems": 1 },
        "runs": [
          { "integration": "checkout", "instance": "5f0c2e1a-…", "run": null, "first_sequence": 0, "last_sequence": 1,
            "records": 5, "clean_stop": true, "stopped_at": "2026-11-13T17:42:10Z" }
        ],
        "problems": [
          { "code": "record_hash_mismatch", "integration": "checkout", "instance": "5f0c2e1a-…", "batch": 0, "index": 1,
            "span_id": "00f067aa0ba902b7", "file": "calls/calls.jsonl", "line": 2,
            "message": "the record does not match its hotline.record.hash" }
        ]
      }

    Scenario: Good records give ok true and no problems
      Given the folder calls from a stopped proxy, with 5 calls
      When I run hotline-proxy verify calls --format json
      Then it exits with 0
      And standard output is one JSON object with format hotline.verify.v1 and ok true
      And its summary counts 5 records, 1 run and 0 problems

    Scenario Outline: Each problem has its own code
      Given the folder calls from a stopped proxy, with calls in 3 batch windows
      When <change>
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And ok is false and the problems hold one with code <code>, the batch, and the file and line where it was found

      Examples:
        | change                                                         | code                    |
        | http.response.status_code is changed in a record               | record_hash_mismatch    |
        | a span's end is changed in a record's trace                    | trace_hash_mismatch     |
        | a record is changed and its hash recomputed                    | batch_root_mismatch     |
        | a record is deleted                                            | record_missing          |
        | hotline.batch.records is changed in a batch record             | batch_signature_invalid |
        | the batch record with sequence 1 is deleted                    | batch_missing           |
        | batch 2's hotline.batch.previous is changed                    | chain_broken            |
        | a record's hotline.batch.index is changed to 99                | record_extra            |
        | batch 0 is in the file twice                                   | chain_broken            |
        | batch 0 names a batch before it                                | chain_broken            |

    Scenario Outline: A changed full record has its own code
      Given the integration has content full and maxBodySize 1MiB
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      And hotline-proxy is stopped
      And <change>
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And ok is false and the problems hold one with code <code>, the batch, and the file and line where it was found

      Examples:
        | change                                      | code                  |
        | the kept url.full is changed in the record  | content_hash_mismatch |
        | the kept request body is changed            | body_hash_mismatch    |

    Scenario: A batch record without a sequence is named by its id
      Given the folder calls from a stopped proxy
      When the first batch record loses its hotline.batch.sequence
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the signature of that batch, named by its id, does not check out

    Scenario Outline: Things that are not changes are notes, not problems
      Given <situation>
      When I run hotline-proxy verify calls --format json
      Then the run or batch has the note <note>

      Examples:
        | situation                                              | note              |
        | the folder calls from a proxy that was killed          | no_last_batch     |
        | the file exporter dropped one record of batch 0        | records_dropped   |
        | a kept body was cut short by maxBodySize               | body_not_checked  |

    Scenario: A problem that comes from the input cannot break the JSON
      Given a record whose url.full holds a quote, a newline and the character U+009B
      And that record was changed
      When I run hotline-proxy verify calls --format json
      Then standard output is still one valid JSON object, with those characters escaped

    Scenario: A body check in JSON
      Given the vendor answered GET /orders with the json body {"orders":[]}
      And the body was saved as reply.json
      When I run hotline-proxy verify calls --body reply.json --span 00f067aa0ba902b7 --side response --format json
      Then the JSON has a body entry with span_id 00f067aa0ba902b7, side response and matches true

    Scenario: Input that cannot be read is still JSON
      When I run hotline-proxy verify a folder that does not exist --format json
      Then it exits with 2
      And standard output is one JSON object with ok false and a problem with code input_unreadable

  Rule: Bad input is refused, not guessed at

    Scenario Outline: Input verify cannot read
      When I run hotline-proxy verify <input>
      Then it exits with 2
      And it prints <message>

      Examples:
        | input                         | message                                   |
        | a folder that does not exist  | that the folder does not exist            |
        | a file that is not JSON lines | the file and line it could not read       |
        | a batch record of format v9   | that hotline.batch.v9 is not a known format |
        | a line with no time           | that line 1 has no time                   |
        | a compressed file that is not gzip | that the compressed file is not gzip |
        | a cut-off compressed file     | that the compressed file ends too soon    |
        | a rotated file that is a folder | that the rotated file cannot be read    |
