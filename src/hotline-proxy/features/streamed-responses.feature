Feature: Streamed responses checked item by item
  A response sent as Server-Sent Events (a series of small events, each with
  a name and a line of data, the way Anthropic and OpenAI stream a model's
  answer) is checked one event at a time, against the itemSchema OpenAPI
  3.2 gives its media type. The events pass on as they come and never wait
  for a check. As for a WebSocket, the call is recorded once the response's
  headers reach the app, each event gets a record and a span of its own,
  and the stream a record of its own when it ends (Peter, 2026-11-21, M2,
  Q1 to Q5 in the project files' streamed-replies/requirements.md).

  The vendor is a fake Anthropic, checked against the spec in
  specs/anthropic/messages. Its response "Hello!" is the one in Anthropic's
  streaming docs: message_start, content_block_start, ping, two
  content_block_delta events, content_block_stop, message_delta and
  message_stop, eight events in all.

  Background:
    Given the Anthropic spec

  # Each event checked

  Scenario: Every event of a good stream is an item record, checked and valid
    Given the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then the app got every byte as the vendor sent it
    And there are 8 item records, each hotline.schema.status valid
    And item record 1 says anthropic.stream.event message_start, hotline.stream.item.index 1
    And item record 8 says anthropic.stream.event message_stop, hotline.stream.item.index 8
    And the stream record says hotline.stream.items 8, hotline.stream.items_invalid 0, hotline.stream.end complete
    And the ledger holds the call first, then the 8 items, then the stream

  Scenario: A broken event says what broke and where in the stream it was
    Given the Anthropic vendor streams the response "Hello!", with event 4's data {"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}
    When the app asks for a streamed message
    Then the app got every byte as the vendor sent it
    And item record 4 says anthropic.stream.event content_block_delta, hotline.schema.status invalid
    And item record 4's response results are body $.data.delta.text required
    And item records 1 to 3 say hotline.schema.status valid
    And item records 5 to 8 say hotline.schema.status valid
    And the stream record says hotline.stream.items 8, hotline.stream.items_invalid 1

  Scenario Outline: What the spec does not allow is reported
    Given the Anthropic vendor streams the bytes <bytes>
    When the app asks for a streamed message
    Then item record 1's response results are <results>

    Examples:
      | bytes                                                      | results                   |
      | "event: surprise\ndata: {\"type\":\"surprise\"}\n\n"       | body $.event enum message_start, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop, ping, error |
      | "event: ping\ndata: not json\n\n"                          | body $.data json          |
      | "data: {\"type\":\"ping\"}\n\n"                            | body $.event required     |
      | "event: ping\ndata: {\"type\":\"pong\"}\n\n"               | body $.data.type const ping |

  # How the events are read: the HTML standard's rules, as OpenAPI 3.2 asks

  Scenario Outline: Events are read by the rules of the HTML standard
    Given the Anthropic vendor streams the bytes <bytes>
    When the app asks for a streamed message
    Then there are <items> item records, each hotline.schema.status valid

    Examples:
      | what                                         | bytes                                                                  | items |
      | a comment is not an event                    | ": keep alive\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"           | 1     |
      | several data lines are joined by a new line  | "event: ping\ndata: {\"type\":\ndata: \"ping\"}\n\n"                   | 1     |
      | lines may end with CR LF                     | "event: ping\r\ndata: {\"type\":\"ping\"}\r\n\r\n"                     | 1     |
      | lines may end with CR alone                  | "event: ping\rdata: {\"type\":\"ping\"}\r\r"                           | 1     |
      | the space after the colon may be left out    | "event:ping\ndata:{\"type\":\"ping\"}\n\n"                             | 1     |
      | an event with no data is not delivered       | "event: ping\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"            | 1     |
      | an unknown field is ignored                  | "event: ping\nflavour: mint\ndata: {\"type\":\"ping\"}\n\n"            | 1     |
      | a byte order mark at the start is dropped    | "﻿event: ping\ndata: {\"type\":\"ping\"}\n\n"                     | 1     |
      | an event left open at the end is dropped     | "event: ping\ndata: {\"type\":\"ping\"}\n\nevent: ping\ndata: {}"      | 1     |

  Scenario: Bytes that only start like a byte order mark are kept
    The first two bytes of the mark, then not the third: they are part of
    the first line, whose field the standard then doesn't know.
    Given the Anthropic vendor streams the bytes "\xef\xbbevent: ping\ndata: {\"type\":\"ping\"}\n\n"
    When the app asks for a streamed message
    Then item record 1's response results are body $.event required

  Scenario: An event split across many pieces is read as one
    Given the Anthropic vendor streams the response "Hello!", one byte at a time
    When the app asks for a streamed message
    Then the app got every byte as the vendor sent it
    And there are 8 item records, each hotline.schema.status valid

  Scenario: retry and id are read as the HTML standard says
    retry is a whole number, kept only when it is all digits; id is text.
    The Anthropic spec says nothing about either, so both pass.
    Given the Anthropic vendor streams the bytes "id: 7\nretry: 3000\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"
    When the app asks for a streamed message
    Then there are 1 item records, each hotline.schema.status valid

  Scenario Outline: retry is kept only when it is all digits
    Given the Anthropic spec, where every event needs a retry
    And the Anthropic vendor streams the bytes <bytes>
    When the app asks for a streamed message
    Then item record 1 says hotline.schema.status <status>

    Examples:
      | what                       | bytes                                                         | status  |
      | digits are kept            | "retry: 3000\nevent: ping\ndata: {\"type\":\"ping\"}\n\n" | valid   |
      | a sign is not a digit      | "retry: +5\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"   | invalid |
      | a unit is not a digit      | "retry: 3s\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"   | invalid |
      | an empty retry is not kept | "retry:\nevent: ping\ndata: {\"type\":\"ping\"}\n\n"      | invalid |

  # The call, the items and the stream

  Scenario: The call is recorded once the response's headers reach the app, before the stream ends
    Given the Anthropic vendor streams message_start, then waits for the app to read it
    When the app asks for a streamed message and reads 1 event
    Then a call record and an item record arrive while the stream is open
    And the stream's call record says http.response.status_code 200, gen_ai.request.model claude-opus-5-5, hotline.schema.status valid

  Scenario: The call record has no response body; the stream record counts and hashes it
    The bytes after the headers are the stream's, not the call's, as for
    a WebSocket after its 101. The stream's hash is keyed with the
    stream's trace and span, so the ledger covers every event.
    Given the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then the stream's call record has no response body, and the hash of no body
    And the stream record's hotline.stream.size is the size of the response "Hello!", and its hotline.stream.hash the hash of it
    And the stream record says hotline.integration.id support, server.address api.anthropic.com

  Scenario: Each item has its own span under the stream, which is under the call
    Given the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then the stream record's span is a "stream" span under the call's "hotline call" span, starting when the call ends
    And each item record's span is a "stream item" span under the "stream" span
    And each "stream item" span has an "item spec check" span under it, with a "wait for check worker" span under that

  Scenario: A response sent whole is checked as before
    The same call without stream: true gets one JSON body, which is
    checked against the schema, with no stream record.
    Given the Anthropic vendor answers the message "Hello!" whole
    When the app asks for a message
    Then the record says hotline.schema.status=valid
    And there are 0 item records
    And there is no stream record

  Scenario: A streamed response whose spec gives no itemSchema is still skipped
    Given the Anthropic spec, without the itemSchema
    And the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then the record says hotline.schema.skipped=streaming
    And there are 0 item records

  # Limits: the stream never waits

  Scenario: An event over 2 MiB is not checked, and the next one still is
    Given the Anthropic vendor streams a content_block_delta of 3 MiB, then the response "Hello!"
    When the app asks for a streamed message
    Then the app got every byte as the vendor sent it
    And item record 1 says hotline.schema.skipped body_too_large, hotline.stream.item.size 3145728
    And item record 2 says anthropic.stream.event message_start, hotline.schema.status valid

  Scenario: A full spec check line drops item checks, never a byte of the stream
    As in spec-check-line.feature, one worker is kept busy by the check of
    a large order to the shop, so the events' checks must wait, and a line
    of 1 KiB holds only a few of them.
    Given the shop spec
    And the vendor answers 201
    And the proxy checks with a single worker
    And specChecks.queueMemory is 1KiB
    And the Anthropic vendor streams the response "Hello!"
    When the app sends a large order, then asks for a streamed message
    Then the app got every byte as the vendor sent it
    And item record 1 says hotline.schema.skipped overloaded
    And item record 8 says hotline.schema.status valid

  Scenario: Without responseBody in checks, items are recorded but not checked
    Given the Anthropic spec, checking only headers and status
    And the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then there are 8 item records
    And item record 1 has no hotline.schema.status, anthropic.stream.event

  # How the stream ended

  Scenario: A stream the vendor breaks off is recorded as cut, without its last part
    Given the Anthropic vendor streams the response "Hello!", breaking off inside event 5
    When the app asks for a streamed message
    Then there are 4 item records
    And the stream record says hotline.stream.items 4, hotline.stream.end cut

  Scenario: A stream still open at stop is ended and recorded
    Given the Anthropic vendor streams message_start, then waits for the app to read it
    When the app asks for a streamed message and reads 1 event
    And the proxy stops
    Then there is one call record and one stream record
    And the stream record says hotline.stream.end cut

  Scenario: A stream that opens while the proxy stops is ended and recorded
    Given the Anthropic vendor holds its headers until the proxy is stopping, then streams message_start
    When the app asks for a streamed message, without waiting for it
    And the proxy stops
    Then there is one call record and one stream record
    And the stream record says hotline.stream.end cut

  # What else sees it

  Scenario: The console shows the call and the stream, not each event
    Given a console exporter
    And the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then the console has 4 lines
    And the third line is the time and "support: stream api.anthropic.com"
    And the fourth line is "<< ended 8 items 0 invalid" with the stream's size and the time it was open

  Scenario: The console shows broken events in red
    Given the console exporter has color always
    And the Anthropic vendor streams the response "Hello!", with event 4's data {"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}
    When the app asks for a streamed message
    Then the fourth line is "<< ended 8 items 1 invalid" with the stream's size and the time it was open
    And the stream's "1 invalid" is red

  Scenario: hotline-proxy verify checks the stream and item records too
    Given a file exporter writing to the folder calls
    And the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    And hotline-proxy is stopped
    And I run hotline-proxy verify calls
    Then it exits with 0

  Scenario: The api key is in no record
    Given the Anthropic vendor streams the response "Hello!"
    When the app asks for a streamed message
    Then no record or span holds the app's api key
