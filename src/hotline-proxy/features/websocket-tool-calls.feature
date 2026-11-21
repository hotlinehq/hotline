Feature: WebSocket messages Kraken has no example of
  Kraken's requests all come from the app, and its correlation id, req_id,
  sits at the top of every message. So the cases the proxy handles but
  Kraken never shows run here, against a small spec of their own, shaped
  like Gemini Live's tool calls, and the Kraken spec is never bent for them
  (Peter, 2026-11-21).

  The tool spec's GET /live answers 101 and lists two operations: toolCall,
  sent by the vendor, with toolResponse as its reply, and toolError, sent by
  the vendor, with no reply. The correlation id of toolCall is at
  /toolCall/functionCalls/0/id, of toolResponse at
  /toolResponse/functionResponses/0/id, and of toolError at /toolError/id.
  A toolError may hold the toolError that caused it, so its schema refers
  to itself. Its GET /other answers 101 too, and lists nothing.

  Background:
    Given the tool spec

  Scenario: A request can come from the vendor
    Given the tool vendor sends toolCall with id "call-7"
    When an app opens the tool WebSocket
    And the app reads a message
    And the app sends toolResponse with id "call-7"
    And the app closes the tool WebSocket
    Then message record 1 says hotline.websocket.message.direction from_vendor, hotline.websocket.message.name toolCall
    And message record 1 and its span say hotline.websocket.operation toolCall, hotline.websocket.message.role request, messaging.message.conversation_id call-7
    And message record 2 says hotline.websocket.message.direction from_app, hotline.websocket.message.name toolResponse
    And message record 2 and its span say hotline.websocket.operation toolCall, hotline.websocket.message.role reply, messaging.message.conversation_id call-7

  Scenario: A correlation id in a list is read at the index the spec names
    Given the tool vendor sends toolCall with ids "call-7" and "call-8"
    When an app opens the tool WebSocket
    And the app reads a message
    And the app closes the tool WebSocket
    Then message record 1 says messaging.message.conversation_id call-7

  Scenario: A correlation id whose path stops at a value that holds no fields records nothing
    Given the tool vendor sends {"toolError":"no such order"}
    When an app opens the tool WebSocket
    And the app reads a message
    And the app closes the tool WebSocket
    Then message record 1 says hotline.websocket.message.name toolError, hotline.websocket.message.role answer
    And message record 1 has no messaging.message.conversation_id

  Scenario: A session whose route has no x-async-api is counted in bytes only
    When an app opens a WebSocket to the tool spec's /other and sends "hello"
    Then there are 0 message records
    And the session record has no hotline.websocket.close.from

  Scenario: A correlation id in an empty list records nothing
    When an app opens the tool WebSocket
    And the app sends {"toolResponse":{"functionResponses":[]}}
    And the app closes the tool WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.websocket.message.role reply
    And message record 1 has no messaging.message.conversation_id

  Scenario: A correlation id in a list is still read from a message too large to check
    The message is read only as far as naming needs: here into the list
    of responses, which the long text inside it cuts short.
    When an app opens the tool WebSocket
    And the app sends a toolResponse of 3 MiB with id "call-9"
    And the app closes the tool WebSocket
    Then message record 1 says hotline.websocket.message.name toolResponse, hotline.schema.skipped body_too_large, messaging.message.conversation_id call-9
