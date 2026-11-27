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
      And the file writes hotline.recorded.shop.order.weight as 2.0 and as 1.5

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

    Scenario: A run with a named instance shows when it started
      Given the records transform labels service.instance.id { env: POD_NAME } and POD_NAME is checkout-0
      And the folder calls from a stopped proxy, with 1 calls
      When I run hotline-proxy verify calls
      Then it exits with 0
      And it prints the instance checkout-0 with the time its run started

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
        | field          | id                             |
        | span_id        | the same id in capital letters |
        | parent_span_id | zeros, which stand for no span |

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

  Rule: Every field the proxy writes is covered

    A record's hash covers every field of its line, not only its
    attributes: its time and observed time, its level, its event, its trace
    flags, its body and its resource (Peter, 2026-11-25). A batch record's
    signature covers the same fields of its own line.

    Scenario Outline: A change to any field of a record shows
      Given the folder calls from a stopped proxy, with 3 calls
      When the <field> of the second record's line is changed
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the record with that span id does not match its hotline.record.hash

      Examples:
        | field                |
        | time                 |
        | observed_time        |
        | severity             |
        | event                |
        | trace_id             |
        | span_id              |
        | flags                |
        | body                 |
        | resource's service.name |

    Scenario: A change to a span's flags shows
      Given the folder calls from a stopped proxy, with 3 calls
      When the flags of the first span in the second line's spans are changed
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the trace of the record with that span id does not match its hotline.trace.hash

    Scenario Outline: A change to any field of a batch record shows
      Given the folder calls from a stopped proxy
      When the <field> of the first batch record's line is changed
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the signature of batch 0 does not check out with its key

      Examples:
        | field                |
        | time                 |
        | observed_time        |
        | severity             |
        | resource's service.name |

  Rule: Only folders written in this format are read

    Scenario Outline: A folder written by an older edge build is refused
      The hashes changed with hotline.record.v2 and hotline.batch.v2, and
      exporters sign their own batch records since hotline.batch.v3. Only
      edge builds wrote the old ones, so no old format is kept (Peter,
      2026-11-25 and 2026-11-26).
      Given the folder calls from a stopped proxy
      When the first batch record's hotline.batch.format is changed to <format>
      And I run hotline-proxy verify calls
      Then it exits with 2
      And it prints that the folder was written by an older edge build (<format>), which this verify cannot check

      Examples:
        | format           |
        | hotline.batch.v1 |
        | hotline.batch.v2 |

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

    Scenario Outline: A deleted record listed in a batch record not signed by the proxy is refused
      A batch record is signed by its exporter with the batch's key, so a
      record deleted and listed as dropped shows.
      Given the folder calls from a stopped proxy, with 3 calls
      When the second record is deleted and listed in the batch record's hotline.batch.dropped_records, <signed>
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the signature of batch 0 does not check out with its key

      Examples:
        | signed                                                   |
        | with the signature left as it was                        |
        | signed with a key of its own, keeping the batch's key    |

    Scenario: A record deleted with another exporter's batch record in place of this copy's shows
      Each copy is one exporter's, and each of its batch records names that
      exporter. The second exporter really dropped record 2 of batch 0; the
      first held it. Deleting record 2 from the first copy and swapping in
      the second exporter's batch record no longer passes as a drop (Peter,
      2026-11-26).
      Given two file exporters, only the second of them dropped one record of batch 0, of batch 1 and of batch 2
      When the first folder's record 2 of batch 0 is deleted and its batch record of batch 0 is swapped for the second folder's
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the batch record of batch 0 is from the exporter calls2, and this copy's other batch records from calls

    Scenario: Another exporter's batch record added to a copy shows
      Given two file exporters, both of them dropped one record of batch 0
      When the second folder's batch record is added to the first folder
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that batch 0 has more than one batch record in this copy: one is from the exporter calls2
      And it prints that 1 problem was found

    Scenario: A batch record listing a record this copy holds is another copy's
      Given two file exporters, only the second of them dropped one record of batch 0
      When the first folder's batch record is swapped for the second folder's
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the batch record of batch 0 lists index 2, which this copy holds

    Scenario: A clean folder says whose copy it is
      Given the folder calls from a stopped proxy
      When I run hotline-proxy verify calls
      Then it exits with 0
      And it prints that the folder is the copy of the exporter calls

    Scenario Outline: A change to the exporter's part of a batch record shows
      Given the file exporter's queue was full for one record of batch 0
      When the <field> of the first batch record's line is changed
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that the signature of batch 0 does not check out with its key

      Examples:
        | field                         |
        | hotline.exporter              |
        | hotline.batch.dropped_records |

  Rule: Each batch's certificate is checked, and said to be the proxy's own

    A batch record carries the certificate of the key that signed it, made
    by the vault and signed by that key itself (Peter, 2026-11-26). It
    names the service and the key's hour. Nothing outside the folder vouches
    for it yet, so verify says so in a note.

    Scenario Outline: A batch certificate that does not check out
      Given the folder calls from a stopped proxy
      When the first batch record's <change>
      And I run hotline-proxy verify calls
      Then it exits with 1
      And it prints that <message>

      Examples:
        | change                                                                    | message                                                                                   |
        | certificate is cut short                                                  | the certificate of batch 0 cannot be read                                                 |
        | certificate is signed by another key                                      | the certificate of batch 0 is not signed by its own key                                   |
        | certificate is swapped for one of another key                             | the signature of batch 0 does not check out with its key                                  |
        | record is signed again with a certificate naming the service another-proxy | the certificate of batch 0 names the service another-proxy, and the batch record hotline-proxy |
        | record is signed again with a certificate whose hour ended before it      | batch 0 was closed outside its certificate's hour                                         |

  Rule: A batch signed under an authority must lead to a root authority you trust

    A root authority is the top authority, which the person checking
    chooses to trust. verify trusts the computer's public root authorities
    and the ones given with --root-authority (Peter, 2026-11-26). It reports
    no levels: a batch is self-signed, or leads to a root you trust. A team
    that runs its own authority checks that the batches are theirs by giving
    their root.

    Scenario: Batches from the proxy's own keys are self-signed
      Given the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --format json
      Then it exits with 0
      And the run's and the summary's batch_signing is self_signed
      And the run or batch has the note key_self_certified

    Scenario Outline: Batches under an authority lead to the root given
      Given <authority>
      And the config signs the hourly keys with the cert batches
      And the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --root-authority root.pem --format json
      Then it exits with 0
      And the run's and the summary's batch_signing is root_authority and root_authorities names "<root>"
      And the report has no note key_self_certified

      Examples:
        | authority                                                                       | root                         |
        | an Ed25519 certificate authority named batches in certs                         | hotline test batch authority |
        | an ECDSA P-256 certificate authority named batches in certs                     | hotline test batch authority |
        | an RSA 2048 certificate authority named batches in certs                        | hotline test batch authority |
        | an ML-DSA-65 certificate authority named batches in certs                       | hotline test batch authority |
        | an Ed25519 certificate authority named batches in certs, below a root authority | hotline test root authority  |

    Scenario: The text output names the root a run's batches lead to
      Given an Ed25519 certificate authority named batches in certs
      And the config signs the hourly keys with the cert batches
      And the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --root-authority root.pem
      Then it exits with 0
      And it prints that checkout's batches lead to the root authority hotline test batch authority

    Scenario: Without its root, an authority is not trusted
      Given an Ed25519 certificate authority named batches in certs
      And the config signs the hourly keys with the cert batches
      And the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --format json
      Then it exits with 1
      And the problems hold one with code batch_authority_untrusted naming "give its root with --root-authority"

    Scenario: A root authority file that cannot be read stops verify
      Given the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --root-authority missing.pem
      Then it exits with 2

    Scenario: A batch closed after its authority expired is refused
      Given a certificate authority named batches in certs that expires in half an hour
      And the config signs the hourly keys with the cert batches
      And the app sends GET /orders
      And 10 seconds pass
      And an hour passes on the proxy's clock
      And the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --root-authority root.pem --format json
      Then it exits with 1
      And the problems hold one with code batch_outside_authority naming "outside the time its authority hotline test batch authority is valid"

    Scenario: A run that gets an authority at a reload is both
      Given the app sends GET /orders
      And 10 seconds pass
      And an Ed25519 certificate authority named batches in certs
      And the signing config is set to the cert batches and reloaded
      And the folder calls from a stopped proxy
      When I run hotline-proxy verify calls --root-authority root.pem --format json
      Then it exits with 0
      And the run's and the summary's batch_signing is both and root_authorities names "hotline test batch authority"

    Scenario Outline: A batch certificate made by hand that does not check out
      Given the folder calls from a stopped proxy
      When the first batch record is signed again by a key whose certificate <made>
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And the problems hold one with code <code> naming "<message>"

      Examples:
        | made                                                                          | code                     | message                           |
        | claims to be a certificate authority                                          | batch_certificate_invalid | is a certificate authority       |
        | an authority signed, with the authorities swapped for another authority's      | batch_authority_unlinked | do not link up                    |
        | a certificate that is not an authority signed, named as its authority          | batch_authority_unlinked | do not link up                    |

  Rule: verify checks every signature algorithm a batch record names

    The proxy's own keys are Ed25519 or ML-DSA; another signer may use ECDSA
    or RSA. These records are made by hand, as such a signer would write
    them (Peter, 2026-11-26).

    Scenario Outline: A signature of each algorithm checks out, and a changed one does not
      Given the folder calls from a stopped proxy
      When the first batch record is signed again by a <algorithm> key, its certificate signed by itself
      And I run hotline-proxy verify calls --format json
      Then it exits with 0
      When one byte of the first batch record's signature is changed
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And ok is false and the problems hold one with code batch_signature_invalid, the batch, and the file and line where it was found

      Examples:
        | algorithm        |
        | ed25519          |
        | ecdsa-sha256     |
        | rsa-pss-sha256   |
        | rsa-pkcs1-sha256 |
        | ml-dsa-44        |
        | ml-dsa-65        |
        | ml-dsa-87        |

  Rule: Bodies are checked against a record

    Scenario: A body the customer kept matches its record
      Given the vendor answered GET /orders with the json body {"orders":[]}
      And the body was saved as response.json
      When I run hotline-proxy verify calls --body response.json --span 00f067aa0ba902b7 --side response
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
        | body          | span             | message                         |
        | calls         | 00f067aa0ba902b7 | that the body cannot be read    |
        | response.json | 0000000000000000 | that no record has that span id |

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
          { "integration": "checkout", "instance": "5f0c2e1a-…", "run_started": "2026-11-13T17:41:50.123456Z", "first_sequence": 0, "last_sequence": 1,
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
      And its summary names the exporter calls

    Scenario Outline: Each problem has its own code
      Given the folder calls from a stopped proxy, with calls in 3 batch windows
      When <change>
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And ok is false and the problems hold one with code <code>, the batch, and the file and line where it was found

      Examples:
        | change                                             | code                    |
        | http.response.status_code is changed in a record   | record_hash_mismatch    |
        | a span's end is changed in a record's trace        | trace_hash_mismatch     |
        | a record is changed and its hash recomputed        | batch_root_mismatch     |
        | a record is deleted                                | record_missing          |
        | hotline.batch.records is changed in a batch record | batch_signature_invalid |
        | the batch record with sequence 1 is deleted        | batch_missing           |
        | batch 2's hotline.batch.previous is changed        | chain_broken            |
        | a record's hotline.batch.index is changed to 99    | record_extra            |
        | a changed copy of batch 0 is in the file           | chain_broken            |
        | a changed copy of a record is in the file          | record_extra            |
        | batch 0 names a batch before it                    | chain_broken            |

    Scenario Outline: A changed full record has its own code
      Given the integration has content full and maxBodySize 1MiB
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      And hotline-proxy is stopped
      And <change>
      And I run hotline-proxy verify calls --format json
      Then it exits with 1
      And ok is false and the problems hold one with code <code>, the batch, and the file and line where it was found

      Examples:
        | change                                     | code                  |
        | the kept url.full is changed in the record | content_hash_mismatch |
        | the kept request body is changed           | body_hash_mismatch    |

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
        | situation                                       | note             |
        | the folder calls from a proxy that was killed   | no_last_batch    |
        | the folder calls from a stopped proxy           | key_self_certified |
        | the file exporter dropped one record of batch 0 | records_dropped  |
        | a kept body was cut short by maxBodySize        | body_not_checked |
        | the folder calls holds an exact copy of a record | records_repeated |
        | the folder calls holds an exact copy of batch 0 | batch_record_repeated |

    Scenario Outline: An exact copy, as a send tried again leaves it, is not a problem
      Given the folder calls holds an exact copy of <what>
      When I run hotline-proxy verify calls --format json
      Then it exits with 0
      And its summary counts 3 records, 1 run and 0 problems

      Examples:
        | what     |
        | a record |
        | batch 0  |

    Scenario: A problem that comes from the input cannot break the JSON
      Given a record whose url.full holds a quote, a newline and the character U+009B
      And that record was changed
      When I run hotline-proxy verify calls --format json
      Then standard output is still one valid JSON object, with those characters escaped

    Scenario: A body check in JSON
      Given the vendor answered GET /orders with the json body {"orders":[]}
      And the body was saved as response.json
      When I run hotline-proxy verify calls --body response.json --span 00f067aa0ba902b7 --side response --format json
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
        | input                              | message                                     |
        | a folder that does not exist       | that the folder does not exist              |
        | a file that is not JSON lines      | the file and line it could not read         |
        | a batch record of format v9        | that hotline.batch.v9 is not a known format |
        | a line with no time                | that line 1 has no time                     |
        | a compressed file that is not gzip | that the compressed file is not gzip        |
        | a cut-off compressed file          | that the compressed file ends too soon      |
        | a rotated file that is a folder    | that the rotated file cannot be read        |
