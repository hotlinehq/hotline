Feature: WebSocket messages checked against the spec
  The proxy reads each message of a WebSocket session as it passes, finds
  which message it is from the x-hotline-match marks in the payload
  schemas of the messages that the 101 operation's x-async-api operations
  list, checks it against that schema, and writes one record and one span
  per message. The bytes pass unchanged and never wait (Peter, 2026-11-19,
  WebSocket slice 2, Q1 to Q27 in the spike). The operations and messages
  are written in AsyncAPI 3's shapes (Peter, 2026-11-21, Q1 to Q9).

  The live spec's GET /live answers 101 and lists four operations:
  toolCall, sent by the vendor, with toolResponse as its reply; toolError,
  sent by the vendor, with no reply; sessionInfo, sent by the app; and
  serverContent, sent by the vendor. toolCall, toolResponse and toolError
  are named by their top-level field, sessionInfo by type with the const
  session.info. The correlation id of toolCall is at
  /toolCall/functionCalls/0/id, of toolResponse at
  /toolResponse/functionResponses/0/id, and of toolError at /toolError/id.

  Background:
    Given the live spec

  # Which message a frame is

  Scenario: A message is named by its marked field and checked
    Given the live vendor sends toolCall with id "call-7"
    When an app opens the live WebSocket
    And the app reads a message
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then there are 2 message records
    And message record 1 says hotline.websocket.message.direction from_vendor, hotline.websocket.message.name toolCall, hotline.schema.status valid
    And message record 2 says hotline.websocket.message.direction from_app, hotline.websocket.message.name toolResponse, hotline.schema.status valid

  Scenario: A broken message is still named, and says what broke
    When an app opens the live WebSocket
    And the app sends {"toolResponse":{"functionResponses":[{"name":"orderStatus","response":{}}]}}
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.status invalid
    And message record 1's request results are body $.toolResponse.functionResponses[0].id required

  Scenario: A marked field with a const must have that value
    When an app opens the live WebSocket
    And the app sends {"type":"session.info","session":{}}
    And the app sends {"type":"session.other","session":{}}
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name sessionInfo
    And message record 2 says hotline.websocket.message.name unknown
    And message record 2 has no hotline.schema.status, hotline.websocket.operation or hotline.websocket.message.role

  Scenario: A message with several marks needs all of them
    Given the live spec, where sessionInfo also marks session.kind with const "text"
    When an app opens the live WebSocket
    And the app sends {"type":"session.info","session":{"kind":"audio"}}
    And the app sends {"type":"session.info","session":{"kind":"text"}}
    And the app sends {"type":"session.info","session":"text"}
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name unknown
    And message record 2 says hotline.websocket.message.name sessionInfo
    And message record 3 says hotline.websocket.message.name unknown

  Scenario: An empty message is unknown
    When an app opens the live WebSocket
    And the app sends an empty message
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name unknown, hotline.websocket.message.size 0

  Scenario Outline: Unclear marks stop the spec from loading
    Given the live spec, where <problem>
    Then the proxy refuses the config: <message>

    Examples:
      | problem                                         | message                                                    |
      | toolCall has no x-hotline-match                 | the message toolCall has no x-hotline-match                |
      | an x-hotline-match sits inside functionCalls    | sits inside a list                                         |
      | toolCallAgain matches on toolCall alone as well | the messages toolCallAgain and toolCall both match the same messages |
      | toolCall's x-hotline-match is yes               | x-hotline-match at /components/schemas/toolCall/properties/toolCall must be true |
      | a field of toolResponse points nowhere          | the $ref #/components/schemas/nothing points nowhere       |
      | toolCall is not JSON Schema                     | cannot be read as JSON Schema                              |
      | x-async-api is a list                           | x-async-api must hold operations                           |
      | x-async-api also holds channels                 | x-async-api has channels, but holds only operations and defaultContentType |
      | toolCall's action is publish                    | the operation toolCall: action must be send or receive     |
      | toolCall's messages is 5                        | the operation toolCall: messages must be a list of $refs to components/x-async-api/messages |
      | sessionInfo lists a message written in place    | the operation sessionInfo: message 1 must be a $ref to components/x-async-api/messages |
      | sessionInfo lists 5                             | the operation sessionInfo: message 1 must be a $ref to components/x-async-api/messages |
      | sessionInfo has no payload                      | the message sessionInfo: payload must be a $ref to components/schemas |
      | sessionInfo lists a message that is not there   | the $ref #/components/x-async-api/messages/nothing points nowhere |
      | sessionInfo's payload is written in place       | the message sessionInfo: payload must be a $ref to components/schemas |
      | serverContent also lists toolCall               | the message toolCall is in the operations serverContent and toolCall: a message belongs to one operation |
      | toolCall's reply names a channel                | the operation toolCall: reply holds only messages          |
      | toolCall's correlationId is in a header         | the message toolCall: correlationId.location $message.header#/id: a WebSocket message has no headers |
      | toolCall's correlationId is not a pointer       | the message toolCall: correlationId.location must start with $message.payload#/ |
      | the app's messages mix JSON and audio/pcm       | the messages the app sends mix the content types application/json and audio/pcm |
      | the route still uses x-hotline-messages         | x-hotline-messages is replaced by x-async-api              |
      | toolCall marks its id x-hotline-id-tuple        | x-hotline-id-tuple is replaced by the message's correlationId |

  Scenario: Binary frames are read as the side's content type
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7" as a binary frame
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.status valid

  Scenario: An overlay changes a message schema
    Given the live spec with an overlay that adds reason to toolResponse's required fields
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then message record 1's request results are body $.toolResponse.functionResponses[0].reason required

  Scenario: A side whose content type can't be read is skipped
    Given the live spec, where the app's messages are audio/pcm
    When an app opens the live WebSocket
    And the app sends 3 bytes of audio as a binary frame
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name unknown, hotline.schema.skipped unsupported_content_type

  Scenario: A message in several frames is one message
    A control frame, such as a ping, may come between the frames.
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7" split into 3 frames, with a ping after the first
    And the app closes the live WebSocket
    Then there are 1 message records
    And message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.status valid

  Scenario: Without messages in checks, messages are named but not checked
    Given the live spec, checking only headers and status
    When an app opens the live WebSocket
    And the app sends {"toolResponse":{"functionResponses":[{"name":"orderStatus","response":{}}]}}
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.websocket.message.index 1
    And message record 1 has no hotline.schema.status, hotline.schema.skipped or hotline.schema.request.errors

  Scenario Outline: Frames the proxy can't read stop the reading of that side
    The bytes still pass unchanged. Each message from then on is recorded
    as unknown, not checked.
    When an app opens the live WebSocket
    And the app sends only <frames>
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then there are <records> message records, each hotline.websocket.message.name unknown
    And the live vendor got every byte as the app sent it

    Examples:
      | frames                                              | records |
      | a continuation frame that no message started        | 2       |
      | a frame marked compressed, with no compression agreed | 2     |
      | a ping of 126 bytes                                 | 1       |
      | a ping in 2 frames                                  | 1       |
      | a text frame inside a message in frames             | 2       |

  Scenario Outline: A connection that ends inside a frame leaves no message record
    When an app opens the live WebSocket
    And the app sends only <part>
    And the app drops its connection without a close message
    Then there are 0 message records
    And the session record says hotline.websocket.close.from none

    Examples:
      | part                                     |
      | 1 byte of a frame header                 |
      | 3 bytes of a masked frame header         |
      | a ping header without its payload        |
      | the first frame of a message in frames   |
      | half a frame's payload                   |
      | a frame longer than any length           |

  Scenario: With checks [messages], messages are checked
    Given the live spec, checking only messages
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.status valid

  # Limits and load

  Scenario: A message over 2 MiB is not checked
    When an app opens the live WebSocket
    And the app sends a toolResponse of 3 MiB
    And the app closes the live WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.skipped body_too_large
    And message record 1 says hotline.websocket.message.size 3145728
    And the live vendor got all 3145728 bytes of the message unchanged

  Scenario: A skipped check still has the message's correlation id
    The correlation id is read in the step that names the message, which
    is never dropped, so it is there whatever happened to the check (Q14).
    When an app opens the live WebSocket
    And the app sends a toolResponse of 3 MiB with id "call-9"
    And the app closes the live WebSocket
    Then message record 1 says hotline.schema.skipped body_too_large, messaging.message.conversation_id call-9

  Scenario: When the copy of a side does not fit in memory, that side is not read from then on
    A copy that waits for the reader counts under specChecks.queueMemory.
    One that can't fit stops the reading of that side for good: that
    message and every later one are recorded as unknown and overloaded
    (Q26). The bytes still pass.
    Given specChecks.queueMemory is 1KiB
    When an app opens the live WebSocket
    And the app sends a toolResponse of 3 MiB, then toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then message records 1 to 2 say hotline.websocket.message.name unknown, hotline.schema.skipped overloaded
    And the session record says hotline.websocket.unread_from_index 1
    And the live vendor got every byte as the app sent it

  # Compression

  Scenario Outline: Compressed messages are unpacked and checked
    Given the live vendor agrees to <mode>
    When an app opens the live WebSocket
    And the app sends 20 toolResponse messages, packed
    And the app closes the live WebSocket
    Then there are 20 message records, each hotline.websocket.message.name toolResponse, hotline.schema.status valid
    And the live vendor got every byte as the app sent it

    Examples:
      | mode                                                                             |
      | permessage-deflate; client_no_context_takeover; server_no_context_takeover       |
      | permessage-deflate                                                               |
      | x-unused, permessage-deflate; client_max_window_bits=15; server_max_window_bits=10 |

  Scenario: A packed message that can't be unpacked stops the reading of that side
    Given the live vendor agrees to permessage-deflate
    When an app opens the live WebSocket
    And the app sends only a packed message that can't be unpacked
    And the app sends 1 toolResponse messages, packed
    And the app closes the live WebSocket
    Then there are 2 message records, each hotline.websocket.message.name unknown

  Scenario: A packed message over 2 MiB unpacked is not checked, and the next one still is
    Given the live vendor agrees to permessage-deflate
    When an app opens the live WebSocket
    And the app sends a packed toolResponse that unpacks to 3 MiB, then 1 toolResponse messages, packed
    And the app closes the live WebSocket
    Then message record 1 says hotline.schema.skipped body_too_large, hotline.websocket.message.unpacked_size 3145728
    And message record 2 says hotline.websocket.message.name toolResponse, hotline.schema.status valid

  # What is written

  Scenario: Each message has its own span under the session
    Given the live vendor sends toolCall with id "call-7"
    When an app opens the live WebSocket
    And the app reads a message
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then each message record's span is a "websocket message" span under the "websocket session" span
    And each "websocket message" span has a "message spec check" span under it, with a "wait for check worker" span under that

  Scenario: A message's record and span go out before the session ends
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7"
    Then a message record and a "websocket message" span arrive while the WebSocket is open

  # Operations, roles and correlation ids

  Scenario: Each message says its operation, its role and its correlation id
    Nothing is paired in the proxy: the trace store groups a request with
    its replies by the correlation id (Q4, Q5). The role comes from the
    spec: a request's operation has a reply, a reply is in an operation's
    reply, and a message with neither is a push (Q6, Q9).
    Given the live vendor sends toolCall with id "call-7"
    When an app opens the live WebSocket
    And the app reads a message
    And the app sends toolResponse with id "call-7"
    And the app sends {"type":"session.info","session":{}}
    And the app closes the live WebSocket
    Then message record 1 and its span say hotline.websocket.operation toolCall, hotline.websocket.message.role request, messaging.message.conversation_id call-7
    And message record 2 and its span say hotline.websocket.operation toolCall, hotline.websocket.message.role reply, messaging.message.conversation_id call-7
    And message record 3 and its span say hotline.websocket.operation sessionInfo, hotline.websocket.message.role push
    And message record 3 has no messaging.message.conversation_id or hotline.websocket.ids

  Scenario: A message with a correlation id that is in no reply answers a request of any operation
    Given the live vendor sends toolError with id "call-7"
    When an app opens the live WebSocket
    And the app reads a message
    And the app closes the live WebSocket
    Then message record 1 and its span say hotline.websocket.operation toolError, hotline.websocket.message.role answer, messaging.message.conversation_id call-7

  Scenario: A correlation id in a list is read at the index the spec names
    Given the live vendor sends toolCall with ids "call-7" and "call-8"
    When an app opens the live WebSocket
    And the app reads a message
    And the app closes the live WebSocket
    Then message record 1 says messaging.message.conversation_id call-7

  Scenario: A correlation id that is a number is recorded as its text, and any other value records nothing
    When an app opens the live WebSocket
    And the app sends {"toolResponse":{"functionResponses":[{"id":42,"name":"orderStatus","response":{}}]}}
    And the app sends {"toolResponse":{"functionResponses":[{"id":{"n":42},"name":"orderStatus","response":{}}]}}
    And the app sends {"toolResponse":{"functionResponses":[]}}
    And the app closes the live WebSocket
    Then message record 1 says messaging.message.conversation_id 42
    And message record 2 has no messaging.message.conversation_id
    And message record 3 has no messaging.message.conversation_id
    And message record 3 says hotline.websocket.message.role reply

  Scenario: The console shows the session, not each message
    Given a console exporter
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then the console has 4 lines

  Scenario: Token counts are copied as sent
    Given the live vendor sends serverContent with totalTokenCount 42, then serverContent with totalTokenCount 57
    When an app opens the live WebSocket
    And the app reads 2 messages
    And the app closes the live WebSocket
    Then message record 1 says gemini.tokens.total 42
    And message record 2 says gemini.tokens.total 57

  Scenario: No message counter
    Given the live vendor sends toolCall with id "call-7"
    When an app opens the live WebSocket
    And the app reads a message
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    Then hotline.requests counts 1 call, with http.response.status_code 101

  Scenario: hotline-proxy verify checks the message records too
    Given a file exporter writing to the folder calls
    When an app opens the live WebSocket
    And the app sends toolResponse with id "call-7"
    And the app closes the live WebSocket
    And hotline-proxy is stopped
    And I run hotline-proxy verify calls
    Then it exits with 0

  # How the session ended

  Scenario: The close message is on the session record and span
    Given the live vendor closes with 1007 "API key not valid"
    When an app opens the live WebSocket
    And the app waits for the vendor to close
    Then the session record and the websocket session span say hotline.websocket.close.from vendor, hotline.websocket.close.code 1007, hotline.websocket.close.reason API key not valid

  Scenario: No close message
    When an app opens the live WebSocket
    And the app drops its connection without a close message
    Then the session record and the websocket session span say hotline.websocket.close.from none, hotline.websocket.closed_first app

  Scenario: Pings and pongs are counted, not recorded
    Given the live vendor answers each ping with a pong
    When an app opens the live WebSocket
    And the app sends 3 pings and reads 3 pongs
    And the app closes the live WebSocket
    Then there are 0 message records
    And the session record and the websocket session span say hotline.websocket.from_app.pings 3, hotline.websocket.from_vendor.pongs 3

  Scenario: A session whose route has no x-async-api is counted in bytes only
    When an app opens a WebSocket to the live spec's /other and sends "hello"
    Then there are 0 message records
    And the session record has no hotline.websocket.close.from
