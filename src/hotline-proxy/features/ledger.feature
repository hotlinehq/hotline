# Agreed with Peter, 2026-11-16. Detail: docs/proxy-ledger.md.
Feature: Records a third party can trust
  Every record, minimal or full, carries a SHA-256 hash of everything it
  holds. Every 10 seconds, or at 65,536 records, the proxy puts the hashes
  of an integration's records into a tree. Each exporter signs the tree's
  root with the proxy's own Ed25519 key, which the vault makes in memory
  every hour. Each integration has its own batches and its own chain. Each
  batch points to the one before, so a missing batch shows. The batch leaves
  as one OTLP log record per exporter, beside the call records. No secret is
  needed to check anything.

  A hash, in plain words: a short fingerprint of some bytes. Anyone can
  compute it, and a change to the bytes changes it. It proves nothing alone;
  the proof is that it rebuilds a root the proxy signed.

  Background:
    Given the shop spec
    And the proxy's clock is a test clock, so a wait of minutes passes at once

  Rule: Every record carries a hash, and no key is needed to check it

    The hash covers every field the proxy sends on the record (Peter,
    2026-11-25): the word hotline.record.v2, then every attribute but the
    hash itself, in name order, then the time, the observed time, the trace
    id, the span id, the trace flags, the level as a number and as text, the
    event name, the body and the resource.

    Scenario: A full record carries a hash and no signature
      Given the vendor answers 201 with "X-Request-Id: r1"
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then the record has hotline.record.hash
      And the record has no hotline.signature or hotline.signature_key_id
      And the hash checks out when rebuilt from the record alone

    Scenario: A minimal record carries a hash and a place in a batch too
      When the app sends GET /orders with no integration ID
      Then the record is minimal
      And the record has hotline.record.hash, hotline.batch.id and hotline.batch.index
      And the hash checks out when rebuilt from the record alone

    Scenario Outline: A change to the record shows
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      And <change> in the received record
      Then the hash no longer checks out

      Examples:
        | change                               |
        | http.response.status_code is changed |
        | url.full is changed                  |
        | the trace_id is changed              |
        | the timestamp is changed             |
        | the observed time is changed         |
        | the level is changed                 |
        | the event name is changed            |
        | the flags are changed                |
        | a body is added                      |
        | a resource label is changed          |
        | hotline.request.body.hash is changed |
        | hotline.trace.hash is changed        |
        | hotline.batch.index is changed       |
        | an attribute is added                |
        | an attribute is removed              |

    Scenario: A changed record whose hash was recomputed no longer fits its batch
      When the app sends GET /orders
      And http.response.status_code is changed and hotline.record.hash recomputed in the received record
      Then the hash checks out
      But the batch's root no longer rebuilds from its records

  Rule: Each body has its own hash, salted with the call's trace id and span id

    The body hash is SHA-256 of: the word hotline.body.v1, the word request
    or response, the trace id (16 bytes), the span id (8 bytes), then the
    body as it passed. The salt is public: it hides that two calls had the
    same body, not one short body from a guesser.

    Scenario: The call's body
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then hotline.request.body.hash is the body hash of the body the vendor got, with the record's trace id and span id
      And the record holds no A1

    Scenario: The response's body
      Given the vendor answers 200 and the json body {"orders":[]}
      When the app sends GET /orders
      Then hotline.response.body.hash is the body hash of the body the app got, with the record's trace id and span id

    Scenario: The same body in two calls gives two different hashes
      Given the vendor answers 200 and the json body {"ok":true}
      When the app sends GET /orders twice
      Then the two records have different hotline.response.body.hash

    Scenario: A body larger than the copy kept for the checks is hashed whole
      When the app sends POST /orders with "Idempotency-Key: k1" and a json body of 3 MiB
      Then hotline.request.body.hash is the body hash of the body the vendor got, with the record's trace id and span id

    Scenario: An answer cut off half way is hashed over the bytes that passed
      Given the vendor closes the connection after 1000 of 5000 promised bytes
      When the app sends GET /orders
      Then hotline.response.body.hash is the body hash of those 1000 bytes

    Scenario: An empty body has a hash too
      Given the vendor answers 201 with no body
      When the app sends GET /orders
      Then hotline.request.body.hash and hotline.response.body.hash are both there
      And each is the body hash of zero bytes, with the record's trace id and span id

    Scenario: A minimal record has both body hashes
      When the app sends POST /orders with no integration ID and a json body
      Then the record is minimal
      And it has hotline.request.body.hash and hotline.response.body.hash

    Scenario: A tunnel relayed untouched is in the ledger, without body hashes
      Given an interception certificate authority named customer in certs, which the connect part picks
      And an app that trusts the interception certificate authority
      And the connect part lists *.example.com in passthrough
      And the app also trusts the vendors' own certificate authority
      When the app opens a tunnel to pinned.example.com:443
      And the app closes its connections
      Then the record has hotline.record.hash, hotline.batch.id and hotline.batch.index
      And the record has no hotline.request.body.hash or hotline.response.body.hash

    Scenario: A failed TLS handshake is in the ledger, without body hashes
      Given an interception certificate authority named customer in certs, which the connect part picks
      And an app that does not trust the interception certificate authority
      When the app opens a tunnel to api.talon.one:443 as integration talon-shop-a
      Then the record has hotline.record.hash, hotline.batch.id and hotline.batch.index
      And the record has no hotline.request.body.hash or hotline.response.body.hash

    Scenario: A connection a tcp listener relays is in the ledger, without body hashes
      Given a certificate authority named customer in certs
      And pinned.example.com on port 6379 answers "+PONG" to what it gets
      And the original destination of the connection is 192.0.2.250:6379
      And the listeners are:
        """
        apps:
          address: tcp://{apps}
        sidecar:
          address: tcp://{sidecar}
          protocol: tcp
          transparent: { integration: ci-lint }
        """
      When the app sends "PING" to the listener sidecar
      Then the record has hotline.record.hash, hotline.batch.id and hotline.batch.index
      And the record has no hotline.request.body.hash or hotline.response.body.hash

  Rule: The call's trace has a hash, and the record's hash covers it

    Scenario: A trace has a hash
      When the app sends GET /orders
      Then the record has hotline.trace.hash
      And the trace hash checks out when rebuilt from the spans alone

    Scenario: A change to the trace shows
      When the app sends GET /orders
      And a span's end is changed in the received trace
      Then the trace hash no longer checks out

  Rule: Records are grouped into signed batches

    The batch's tree is built as in Certificate Transparency (RFC 6962): a
    record's leaf is SHA-256 of 0x00 and its hash, an inner node is SHA-256
    of 0x01, the left and the right. Each exporter signs its own batch
    record, with the batch's key, which the ledger hands it beside the batch
    (Peter, 2026-11-26). The signature covers the batch record as a record's
    hash covers a record, with the word hotline.batch.v3: every attribute but
    the signature, then its time, observed time, level, event name and
    resource.

    Scenario: Each record names its batch and its place in it
      When the app sends GET /orders 3 times
      Then the records have the same hotline.batch.id, a UUID version 7
      And their hotline.batch.index are 0, 1 and 2, in the order they were written

    Scenario: A batch record follows its records through the exporter
      When the app sends GET /orders 3 times
      And 10 seconds pass
      Then the exporter got a log record with event.name hotline.ledger.batch
      And it has hotline.batch.format hotline.batch.v3, hotline.batch.records 3 and hotline.batch.root
      And the root rebuilds from the 3 records' hashes
      And hotline.batch.signature checks out with the key of hotline.batch.certificate

    Scenario: A batch closes early when it is full
      Given ledger batches hold at most 3 records
      When the app sends GET /orders 4 times
      Then the first 3 records are in one batch and the 4th in the next

    Scenario: No calls in a batch window, no batch record
      When the app sends GET /orders
      And 30 seconds pass with no more calls
      Then the exporter got 1 batch record, with hotline.batch.records 1
      And no integration sent a batch record for the windows without calls

    Scenario: The sequence counts only the batches that were sent
      When the app sends GET /orders
      And 30 seconds pass with no calls
      And the app sends GET /orders
      And 10 seconds pass
      Then the two batch records have hotline.batch.sequence 0 and 1, and the second's previous is the root of the first

    Scenario: Batches form a chain
      When the app sends GET /orders in each of 3 batch windows
      Then the checkout batch records have hotline.batch.sequence 0, 1 and 2, with one hotline.batch.instance
      And batch 0 has no hotline.batch.previous
      And each later batch's hotline.batch.previous is the root of the batch before

    Scenario: Each integration has its own chain
      Given the integrations checkout and refunds
      When the app sends GET /orders as checkout and GET /refunds as refunds
      And 10 seconds pass
      Then there is one batch record with hotline.batch.integration checkout and one with refunds
      And each holds only its own integration's record
      And each chain starts at sequence 0

    Scenario: Calls with no known integration go in the not-passed chain
      When the app sends GET /orders with no integration ID
      And 10 seconds pass
      Then the minimal record is in a batch with hotline.batch.integration not-passed

    Scenario: An integration added by a reload starts its own chain
      Given the integration checkout
      When the app sends GET /orders as checkout
      And 10 seconds pass
      And refunds is added to the config and reloaded
      And the app sends GET /orders as checkout and GET /refunds as refunds
      And 10 seconds pass
      Then the refunds chain starts at sequence 0
      And the checkout chain goes on unbroken

    Scenario: An integration removed by a reload closes its chain
      Given the integrations checkout and refunds
      When the app sends GET /refunds as refunds
      And refunds is removed from the config and reloaded
      Then the refunds chain ends with a batch with hotline.batch.last true

    # When the chain's batch already closed, its last batch is sent empty.
    # It names the config of the chain's batches, not the new config, which
    # has no refunds (Peter, 2026-11-17, from the decomplect review).
    Scenario: An empty last batch names the config its chain had
      Given the integrations checkout and refunds
      When the app sends GET /refunds as refunds
      And 10 seconds pass
      And refunds is removed from the config and reloaded
      Then the refunds chain ends with an empty last batch of the config of the batch before it

    # A record is made after the response has gone back, so a reload can come
    # between a call's start and its record. The record goes in a batch of
    # the config the call started with (Peter, 2026-11-16).
    Scenario: A call in flight during a reload is batched with the config it started with
      Given the integration checkout
      And the vendor holds its answer to the next call
      When the app sends GET /orders as checkout, which waits for the vendor
      And the config is changed and reloaded
      And the vendor answers the call it held
      And the app sends GET /orders as checkout
      And 10 seconds pass
      Then the two records are in two batches of the checkout chain, one after the other
      And each batch's hotline.batch.config is the config of the record in it

    Scenario: The first record after a reload starts a new batch, even within the window
      Given the integration checkout
      When the app sends GET /orders as checkout
      And the config is changed and reloaded
      And the app sends GET /orders as checkout
      And 10 seconds pass
      Then the two records are in two batches of the checkout chain, one after the other
      And each batch's hotline.batch.config is the config of the record in it

    Scenario: A call in flight when its integration is removed stays in its integration's chain
      Given the integrations checkout and refunds
      When the app sends GET /refunds as refunds
      And the vendor holds its answer to the next call
      And the app sends GET /refunds as refunds, which waits for the vendor
      And refunds is removed from the config and reloaded
      And the vendor answers the call it held
      And 10 seconds pass
      Then the refunds chain goes on unbroken after its last batch
      And the held call's record is in the refunds chain, after the last batch

    Scenario: Batch ids sort by time
      When the app sends GET /orders in each of 3 batch windows
      Then the batch ids, sorted as text, are in sequence order

    Scenario: The defaults
      Given no ledger section in the config
      When hotline-proxy starts
      Then its batch settings are every 10 seconds and at most 65,536 records

    Scenario: A batch carries the version and the config hash
      When the app sends GET /orders
      And 10 seconds pass
      Then the batch record has hotline.batch.version, the proxy's version
      And hotline.batch.config is the SHA-256 of the config and its specs as loaded, which hotline.config.version is cut from

    Scenario: A secret read from a file does not change the config hash
      Given the OTLP exporter's Authorization header is read from a file
      When the app sends GET /orders
      And the file's token changes and the proxy restarts
      And the app sends GET /orders
      Then hotline.batch.config is the same as before

    Scenario: A config reload changes the config hash from the next batch on
      When the app sends GET /orders
      And 10 seconds pass
      And the config is changed and reloaded
      And the app sends GET /orders
      And 10 seconds pass
      Then the batch after the reload has a different hotline.batch.config
      And it still follows the chain of the batches before

    Scenario: The call and the response are not changed
      When the app sends GET /orders
      Then the vendor got no header it would not have got without the proxy
      And the app got no header it would not have got without the proxy

  Rule: The proxy makes its own key, in memory, every hour

    Scenario: The key is new on every start and never written to disk
      When the app sends GET /orders
      And hotline-proxy is stopped and started again
      And the app sends GET /orders
      And hotline-proxy is stopped
      Then the two runs sign with different hotline.batch.certificate
      And no file the proxy wrote holds a private key

    Scenario: Each key comes with a certificate it signed itself, for its hour
      A self-signed certificate vouches for nothing outside the folder yet,
      but it gives the key a name and an hour, so a certificate from a
      trusted source can take its place later (Peter, 2026-11-26).
      When the app sends GET /orders
      And 10 seconds pass
      Then the batch record's hotline.batch.certificate is an X.509 certificate for an Ed25519 key, signed by that key
      And the batch record's certificate names the service hotline-proxy
      And it is valid from the proxy's start for one hour

    Scenario: The certificate names the service the labels give
      Given the records transform labels service.name shop-proxy
      When the app sends GET /orders
      And 10 seconds pass
      Then the batch record's certificate names the service shop-proxy

    Scenario: The key changes every hour within a run
      When the app sends GET /orders
      And 10 seconds pass
      And an hour passes on the proxy's clock
      And the app sends GET /orders
      And 10 seconds pass
      Then the batches after it have a new hotline.batch.certificate
      And the chain goes on unbroken across the change

  Rule: A run has a name, and a clean stop is marked

    Scenario: The instance is random per run
      Given no ledger instance in the config
      When the app sends GET /orders
      And hotline-proxy is stopped and started again
      And the app sends GET /orders
      And hotline-proxy is stopped
      Then the two runs' hotline.batch.instance are different
      And each is that run's service.instance.id
      And the batches have no hotline.batch.run

    Scenario: The instance can be named in the config, with the random run id kept next to it
      Given the config has ledger instance { env: POD_NAME } and POD_NAME is invoices-proxy-0
      When the app sends GET /orders
      And hotline-proxy is stopped and started again
      And the app sends GET /orders
      And hotline-proxy is stopped
      Then both runs' batches have hotline.batch.instance invoices-proxy-0
      And their hotline.batch.run are different
      And each run's sequence starts at 0

    Scenario: A clean stop closes one last batch
      When the app sends GET /orders
      And hotline-proxy is stopped
      Then the last batch record has hotline.batch.last true and covers that call
      And no other batch has hotline.batch.last

    Scenario: The last batch is sent on a clean stop even with no calls in it
      When the app sends GET /orders
      And 10 seconds pass
      And hotline-proxy is stopped
      Then the last batch record has hotline.batch.records 0 and hotline.batch.last true

    Scenario: A crash leaves no last batch
      When the app sends GET /orders
      And 10 seconds pass
      And hotline-proxy is killed
      Then no batch record has hotline.batch.last

  Rule: Each exporter signs its own batch record, naming itself and what it dropped

    The ledger builds each batch and signs nothing. Every exporter adds its
    own name, hotline.exporter, and the records of the batch it dropped,
    hotline.batch.dropped_records (empty when it dropped none), and signs
    the whole batch record with the batch's key. So each copy holds one
    batch record per batch, and a copy mixed from two exporters shows
    (Peter, 2026-11-26).

    Scenario: Every exporter sends its own batch record of the same batch
      Given an OTLP exporter and a file exporter
      When the app sends GET /orders 3 times
      And 10 seconds pass
      Then each exporter got one batch record, at level INFO, naming itself in hotline.exporter
      And both batch records have an empty hotline.batch.dropped_records
      And both batch records have the same hotline.batch.id, hotline.batch.root and hotline.batch.certificate
      And each batch record's hotline.batch.signature checks out with the key of its hotline.batch.certificate

    Scenario: A record dropped by one exporter is listed only in that exporter's batch record
      Given an OTLP exporter and a file exporter
      And the file exporter's queue is full for one record
      When the app sends GET /orders 3 times
      And 10 seconds pass
      Then the file got 2 records and a batch record at level WARN whose hotline.batch.dropped_records lists the index and hash of the third
      And the file's batch record's hotline.batch.signature checks out with the key of its hotline.batch.certificate
      And the root rebuilds from the 2 records and the dropped hash
      And the OTLP exporter's batch record lists no dropped record
      And no exporter got a record with event.name hotline.ledger.dropped

    Scenario: Above 1,024 dropped records only the count is sent
      Given the file exporter's queue is full for 1,025 records
      When the app sends GET /orders 1,025 times
      And 10 seconds pass
      Then the file's batch record has hotline.batch.dropped_count 1025 and no hotline.batch.dropped_records
      And that batch cannot be rebuilt from the file's copy

    Scenario: A batch record waits for room and is not dropped
      Given the file exporter's queue is full
      When the app sends GET /orders
      And 10 seconds pass
      And the queue has room again
      Then the file got the batch record

    Scenario: At most 64 batch records wait; the oldest are let go
      The chain shows the batches that are missing, so the batch records
      say nothing more about them (Peter, 2026-11-25).
      Given the OTLP exporter's collector is down
      When the app sends GET /orders in each of 66 batch windows
      And the collector is back
      Then the collector got the newest 64 waiting batch records
      And none of them has hotline.batch.lost_batches
      And the proxy's memory stayed bounded
