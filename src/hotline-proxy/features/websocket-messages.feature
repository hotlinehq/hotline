Feature: WebSocket messages checked against the spec
  The proxy reads each message of a WebSocket session as it passes, finds
  which message it is from the x-hotline-match marks in the payload
  schemas of the messages that the 101 operation's x-async-api operations
  list, checks it against that schema, and writes one record and one span
  per message. The bytes pass unchanged and never wait (Peter, 2026-11-19,
  WebSocket slice 2, Q1 to Q27 in the spike). The operations and messages
  are written in AsyncAPI 3's shapes (Peter, 2026-11-21, Q1 to Q9).

  The vendor is a fake Kraken, checked against the real Kraken spec in
  specs/kraken: Spot WebSocket v2, market data and trading, one GET /v2
  that answers 101 (Peter, 2026-11-21). The app sends requests, such as
  add_order, ping and subscribe; Kraken answers each with a reply carrying
  the request's req_id, the correlation id, and sends pushes of its own,
  such as heartbeat. A failed subscribe (subscribeFailed) answers a request
  of any operation. Every message is named by its constant fields, such as
  method add_order, or method subscribe with params.channel book. Cases
  Kraken has no message for are in websocket-tool-calls.feature.

  Background:
    Given the Kraken spec

  # Which message a frame is

  Scenario: A message is named by its marked fields and checked
    Given the Kraken vendor answers the app's first message with add_order's reply with req_id 7
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app reads a message
    And the app closes the Kraken WebSocket
    Then there are 2 message records
    And message record 1 says hotline.websocket.message.direction from_app, hotline.websocket.message.name addOrder, hotline.schema.status valid
    And message record 2 says hotline.websocket.message.direction from_vendor, hotline.websocket.message.name addOrderReply, hotline.schema.status valid

  Scenario: A broken message is still named, and says what broke
    An order without a side is one of the broken examples Kraken's own
    reference refuses.
    When an app opens the Kraken WebSocket
    And the app sends {"method":"add_order","req_id":7,"params":{"order_type":"limit","order_qty":1.2,"limit_price":97000.5,"symbol":"BTC/USD","token":"t-not-real"}}
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name addOrder, hotline.schema.status invalid
    And message record 1's request results are body $.params.side required

  Scenario: A marked field with a const must have that value
    When an app opens the Kraken WebSocket
    And the app sends {"method":"ping","req_id":1}
    And the app sends {"method":"pang","req_id":2}
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name ping
    And message record 2 says hotline.websocket.message.name unknown
    And message record 2 has no hotline.schema.status, hotline.websocket.operation or hotline.websocket.message.role

  Scenario: A message with several marks needs all of them
    A subscribe is named by its method and by the channel in its params.
    When an app opens the Kraken WebSocket
    And the app sends {"method":"subscribe","req_id":1,"params":{"channel":"news","symbol":["BTC/USD"]}}
    And the app sends {"method":"subscribe","req_id":2,"params":{"channel":"book","symbol":["BTC/USD"]}}
    And the app sends {"method":"subscribe","req_id":3,"params":"book"}
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name unknown
    And message record 2 says hotline.websocket.message.name subscribeBook
    And message record 3 says hotline.websocket.message.name unknown

  Scenario: An empty message is unknown
    When an app opens the Kraken WebSocket
    And the app sends an empty message
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name unknown, hotline.websocket.message.size 0

  Scenario Outline: Unclear marks stop the spec from loading
    Given the Kraken spec, where <problem>
    Then the proxy refuses the config: <message>

    Examples:
      | problem                                            | message                                                                                                    |
      | ping has no x-hotline-match                        | the message ping has no x-hotline-match                                                                    |
      | an x-hotline-match sits inside batch_add's orders  | sits inside a list                                                                                         |
      | pingAgain matches on method ping as well           | the messages ping and pingAgain both match the same messages                                               |
      | ping's x-hotline-match is yes                      | x-hotline-match at /components/schemas/ping/properties/method must be true                                 |
      | a field of add_order points nowhere                | the $ref #/components/schemas/nothing points nowhere                                                       |
      | add_order is not JSON Schema                       | cannot be read as JSON Schema                                                                              |
      | x-async-api is a list                              | x-async-api must hold operations                                                                           |
      | x-async-api also holds channels                    | x-async-api has channels, but holds only operations and defaultContentType                                 |
      | ping's action is publish                           | the operation ping: action must be send or receive                                                         |
      | ping's messages is 5                               | the operation ping: messages must be a list of $refs to components/x-async-api/messages                    |
      | heartbeat lists a message written in place         | the operation heartbeat: message 1 must be a $ref to components/x-async-api/messages                       |
      | heartbeat lists 5                                  | the operation heartbeat: message 1 must be a $ref to components/x-async-api/messages                       |
      | heartbeat has no payload                           | the message heartbeat: payload must be a $ref to components/schemas                                        |
      | heartbeat's payload points nowhere                 | the $ref #/components/schemas/nothing points nowhere                                                       |
      | heartbeat lists a message that is not there        | the $ref #/components/x-async-api/messages/nothing points nowhere                                          |
      | heartbeat's payload is written in place            | the message heartbeat: payload must be a $ref to components/schemas                                        |
      | status also lists heartbeat                        | the message heartbeat is in the operations heartbeat and status: a message belongs to one operation       |
      | ping's reply names a channel                       | the operation ping: reply holds only messages                                                              |
      | ping's correlationId is in a header                | the message ping: correlationId.location $message.header#/req_id: a WebSocket message has no headers       |
      | ping's correlationId is not a pointer              | the message ping: correlationId.location must start with $message.payload#/                                |
      | the app's messages mix JSON and audio/pcm          | the messages the app sends mix the content types application/json and audio/pcm                           |
      | the vendor's messages mix JSON and audio/pcm       | the messages the vendor sends mix the content types application/json and audio/pcm                         |
      | the route still uses x-hotline-messages            | x-hotline-messages is replaced by x-async-api                                                              |
      | ping marks its req_id x-hotline-id-tuple           | x-hotline-id-tuple is replaced by the message's correlationId                                              |
      | ping has traits                                    | the operation ping: traits are not read: write what they add into the operation itself                     |
      | heartbeat has traits                               | the message heartbeat: traits are not read: write what they add into the message itself                    |
      | heartbeat's schemaFormat is Avro                   | the message heartbeat: schemaFormat application/vnd.apache.avro;version=1.9.0 is not JSON Schema           |
      | heartbeat's payload has the schemaFormat Protobuf  | the message heartbeat: schemaFormat application/x-protobuf is not JSON Schema                              |

  Scenario Outline: A schemaFormat of the JSON Schema family loads
    AsyncAPI's and OpenAPI's schema formats and JSON Schema itself are
    all read as JSON Schema (Peter, 2026-11-21, M1).
    Given the Kraken spec, where add_order's schemaFormat is <format>
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name addOrder, hotline.schema.status valid

    Examples:
      | format                                          |
      | application/vnd.aai.asyncapi+json;version=3.0.0 |
      | application/vnd.oai.openapi;version=3.2.0       |
      | application/schema+json;version=draft-07        |

  Scenario: Binary frames are read as the side's content type
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7 as a binary frame
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name addOrder, hotline.schema.status valid

  Scenario: An overlay changes a message schema
    Given the Kraken spec with an overlay that adds cl_ord_id to add_order's required params
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then message record 1's request results are body $.params.cl_ord_id required

  Scenario: A side whose content type can't be read is skipped
    Given the Kraken spec, where every message is audio/pcm
    When an app opens the Kraken WebSocket
    And the app sends 3 bytes of audio as a binary frame
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name unknown, hotline.schema.skipped unsupported_content_type

  Scenario: A message in several frames is one message
    A control frame, such as a ping, may come between the frames.
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7 split into 3 frames, with a ping after the first
    And the app closes the Kraken WebSocket
    Then there are 1 message records
    And message record 1 says hotline.websocket.message.name addOrder, hotline.schema.status valid

  Scenario: Without messages in checks, messages are named but not checked
    Given the Kraken spec, checking only headers and status
    When an app opens the Kraken WebSocket
    And the app sends {"method":"add_order","req_id":7,"params":{"order_type":"limit","order_qty":1.2,"symbol":"BTC/USD","token":"t-not-real"}}
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name addOrder, hotline.websocket.message.index 1
    And message record 1 has no hotline.schema.status, hotline.schema.skipped or hotline.schema.request.errors

  Scenario Outline: Frames the proxy can't read stop the reading of that side
    The bytes still pass unchanged. Each message from then on is recorded
    as unknown, not checked.
    When an app opens the Kraken WebSocket
    And the app sends only <frames>
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then there are <records> message records, each hotline.websocket.message.name unknown
    And the Kraken vendor got every byte as the app sent it

    Examples:
      | frames                                                | records |
      | a continuation frame that no message started          | 2       |
      | a frame marked compressed, with no compression agreed | 2       |
      | a ping of 126 bytes                                   | 1       |
      | a ping in 2 frames                                    | 1       |
      | a text frame inside a message in frames               | 2       |

  Scenario Outline: A connection that ends inside a frame leaves no message record
    When an app opens the Kraken WebSocket
    And the app sends only <part>
    And the app drops its connection without a close message
    Then there are 0 message records
    And the session record says hotline.websocket.close.from none

    Examples:
      | part                                   |
      | 1 byte of a frame header               |
      | 3 bytes of a masked frame header       |
      | a ping header without its payload      |
      | the first frame of a message in frames |
      | half a frame's payload                 |
      | a frame longer than any length         |

  Scenario: With checks [messages], messages are checked
    Given the Kraken spec, checking only messages
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name addOrder, hotline.schema.status valid

  # Limits and load

  Scenario: A message over 2 MiB is not checked
    When an app opens the Kraken WebSocket
    And the app sends a batch_add of 3 MiB
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.websocket.message.name batchAdd, hotline.schema.skipped body_too_large
    And message record 1 says hotline.websocket.message.size 3145728
    And the Kraken vendor got all 3145728 bytes of the message unchanged

  Scenario: A skipped check still has the message's correlation id
    The correlation id is read in the step that names the message, which
    is never dropped, so it is there whatever happened to the check (Q14).
    When an app opens the Kraken WebSocket
    And the app sends a batch_add of 3 MiB with req_id 9
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.schema.skipped body_too_large, messaging.message.conversation_id 9

  Scenario: When the copy of a side does not fit in memory, that side is not read from then on
    A copy that waits for the reader counts under specChecks.queueMemory.
    One that can't fit stops the reading of that side for good: that
    message and every later one are recorded as unknown and overloaded
    (Q26). The bytes still pass.
    Given specChecks.queueMemory is 1KiB
    When an app opens the Kraken WebSocket
    And the app sends a batch_add of 3 MiB, then add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then message records 1 to 2 say hotline.websocket.message.name unknown, hotline.schema.skipped overloaded
    And the session record says hotline.websocket.unread_from_index 1
    And the Kraken vendor got every byte as the app sent it

  # Compression

  Scenario Outline: Compressed messages are unpacked and checked
    Given the Kraken vendor agrees to <mode>
    When an app opens the Kraken WebSocket
    And the app sends 20 add_order messages, packed
    And the app closes the Kraken WebSocket
    Then there are 20 message records, each hotline.websocket.message.name addOrder, hotline.schema.status valid
    And the Kraken vendor got every byte as the app sent it

    Examples:
      | mode                                                                               |
      | permessage-deflate; client_no_context_takeover; server_no_context_takeover         |
      | permessage-deflate                                                                 |
      | x-unused, permessage-deflate; client_max_window_bits=15; server_max_window_bits=10 |

  Scenario: A packed message that can't be unpacked stops the reading of that side
    Given the Kraken vendor agrees to permessage-deflate
    When an app opens the Kraken WebSocket
    And the app sends only a packed message that can't be unpacked
    And the app sends 1 add_order messages, packed
    And the app closes the Kraken WebSocket
    Then there are 2 message records, each hotline.websocket.message.name unknown

  Scenario: A packed message over 2 MiB unpacked is not checked, and the next one still is
    Given the Kraken vendor agrees to permessage-deflate
    When an app opens the Kraken WebSocket
    And the app sends a packed batch_add that unpacks to 3 MiB, then 1 add_order messages, packed
    And the app closes the Kraken WebSocket
    Then message record 1 says hotline.schema.skipped body_too_large, hotline.websocket.message.unpacked_size 3145728
    And message record 2 says hotline.websocket.message.name addOrder, hotline.schema.status valid

  # What is written

  Scenario: Each message has its own span under the session
    Given the Kraken vendor answers the app's first message with add_order's reply with req_id 7
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app reads a message
    And the app closes the Kraken WebSocket
    Then each message record's span is a "websocket message" span under the "websocket session" span
    And each "websocket message" span has a "message spec check" span under it, with a "wait for check worker" span under that

  Scenario: A message's record and span go out before the session ends
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    Then a message record and a "websocket message" span arrive while the WebSocket is open

  # Operations, roles and correlation ids

  Scenario: Each message says its operation, its role and its correlation id
    Nothing is paired in the proxy: the trace store groups a request with
    its replies by the correlation id (Q4, Q5). The role comes from the
    spec: a request's operation has a reply, a reply is in an operation's
    reply, and a message with neither is a push (Q6, Q9).
    Given the Kraken vendor answers the app's first message with add_order's reply with req_id 7, then a heartbeat
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app reads 2 messages
    And the app closes the Kraken WebSocket
    Then message record 1 and its span say hotline.websocket.operation addOrder, hotline.websocket.message.role request, messaging.message.conversation_id 7
    And message record 2 and its span say hotline.websocket.operation addOrder, hotline.websocket.message.role reply, messaging.message.conversation_id 7
    And message record 3 and its span say hotline.websocket.operation heartbeat, hotline.websocket.message.role push
    And message record 3 has no messaging.message.conversation_id or hotline.websocket.ids

  Scenario: One request gets a reply for each order it names
    Kraken answers a cancel_order once per order, each reply with the
    request's req_id.
    Given the Kraken vendor answers the app's first message with cancel_order's reply with req_id 8 for "OQCLML-BW3P3-BUCMWZ", then cancel_order's reply with req_id 8 for "OB5VMB-B4U2U-DK2WRW"
    When an app opens the Kraken WebSocket
    And the app sends {"method":"cancel_order","req_id":8,"params":{"order_id":["OQCLML-BW3P3-BUCMWZ","OB5VMB-B4U2U-DK2WRW"],"token":"t-not-real"}}
    And the app reads 2 messages
    And the app closes the Kraken WebSocket
    Then there are 3 message records
    And message record 1 says hotline.websocket.message.name cancelOrder, hotline.websocket.message.role request, messaging.message.conversation_id 8, hotline.schema.status valid
    And message records 2 to 3 say hotline.websocket.message.name cancelOrderReply, hotline.websocket.message.role reply, messaging.message.conversation_id 8, hotline.schema.status valid

  Scenario: A message with a correlation id that is in no reply answers a request of any operation
    A failed subscribe can't say which channel it was for, so it is listed
    under no subscribe's reply; its req_id ties it to the request.
    Given the Kraken vendor answers the app's first message with a failed subscribe with req_id 7
    When an app opens the Kraken WebSocket
    And the app sends {"method":"subscribe","req_id":7,"params":{"channel":"book","symbol":["NOPE/USD"]}}
    And the app reads a message
    And the app closes the Kraken WebSocket
    Then message record 2 and its span say hotline.websocket.operation subscribeFailed, hotline.websocket.message.role answer, messaging.message.conversation_id 7

  Scenario: A correlation id that is a number is recorded as its text, and any other value records nothing
    When an app opens the Kraken WebSocket
    And the app sends {"method":"ping","req_id":42}
    And the app sends {"method":"ping","req_id":{"n":42}}
    And the app sends {"method":"ping"}
    And the app closes the Kraken WebSocket
    Then message record 1 says messaging.message.conversation_id 42
    And message record 2 has no messaging.message.conversation_id
    And message record 3 has no messaging.message.conversation_id
    And message record 3 says hotline.websocket.message.role request

  Scenario: The console shows the session, not each message
    Given a console exporter
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    Then the console has 4 lines

  Scenario: A field marked x-hotline-record is copied as sent
    The spec copies the order id of an add_order reply.
    Given the Kraken vendor answers the app's first message with add_order's reply with req_id 7 for "OQCLML-BW3P3-BUCMWZ"
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app reads a message
    And the app closes the Kraken WebSocket
    Then message record 2 says kraken.order.id OQCLML-BW3P3-BUCMWZ
    And message record 1 has no kraken.order.id

  Scenario: hotline-proxy verify checks the message records too
    Given a file exporter writing to the folder calls
    When an app opens the Kraken WebSocket
    And the app sends add_order with req_id 7
    And the app closes the Kraken WebSocket
    And hotline-proxy is stopped
    And I run hotline-proxy verify calls
    Then it exits with 0

  # How the session ended

  Scenario: The close message is on the session record and span
    Given the Kraken vendor closes with 1008 "Session token expired"
    When an app opens the Kraken WebSocket
    And the app waits for the vendor to close
    Then the session record and the websocket session span say hotline.websocket.close.from vendor, hotline.websocket.close.code 1008, hotline.websocket.close.reason Session token expired

  Scenario: No close message
    When an app opens the Kraken WebSocket
    And the app drops its connection without a close message
    Then the session record and the websocket session span say hotline.websocket.close.from none, hotline.websocket.closed_first app

  Scenario: Pings and pongs are counted, not recorded
    These are the WebSocket's own control frames, not Kraken's ping
    message.
    Given the Kraken vendor answers each ping with a pong
    When an app opens the Kraken WebSocket
    And the app sends 3 pings and reads 3 pongs
    And the app closes the Kraken WebSocket
    Then there are 0 message records
    And the session record and the websocket session span say hotline.websocket.from_app.pings 3, hotline.websocket.from_vendor.pongs 3
