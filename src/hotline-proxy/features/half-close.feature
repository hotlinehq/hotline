Feature: A relayed tunnel stops each direction on its own
  A tunnel the proxy relays untouched carries two directions: from the app
  to the vendor, and back. When one side stops sending, the proxy tells the
  other side that nothing more comes from there, and keeps the other
  direction open. So a reply still on its way, or bytes the app still
  sends, are never lost (review, Peter 2026-11-12). This holds for ordinary
  tunnels over HTTP/1.1 and for captured tunnels. Over HTTP/2 only the app
  can stop first: the reply of an HTTP/2 stream ends only with the whole
  stream, so when the vendor stops first the tunnel ends.

  Background:
    Given an interception issuer named customer in certs, which the connect part picks
    And the capture listener is a socket file, whose issuer is customer too
    And the connect part lists pinned.example.com in passthrough
    And the capture listener lists pinned.example.com in passthrough

  Scenario Outline: The app stops sending first and still gets the whole reply
    Given pinned.example.com answers only after the app stops sending
    When the app sends "REQUEST" through <tunnel> to pinned.example.com, then stops sending
    Then the app gets "REPLY to REQUEST"

    Examples:
      | tunnel                        |
      | a tunnel opened over HTTP/1.1 |
      | a tunnel opened over HTTP/2   |
      | a captured tunnel             |

  Scenario Outline: The vendor stops sending first and still gets what the app sends after
    Given pinned.example.com sends "HELLO", stops sending, then reads what comes
    When the app reads "HELLO" through <tunnel> from pinned.example.com, then sends "MORE" and stops sending
    Then pinned.example.com got "MORE"

    Examples:
      | tunnel                        |
      | a tunnel opened over HTTP/1.1 |
      | a captured tunnel             |

  Scenario: Over HTTP/2, the vendor stopping first ends the tunnel
    The app still sends nothing more and waits for the end, as many do. The
    proxy ends the stream, so the app is never left waiting.
    Given pinned.example.com sends "HELLO", stops sending, then reads what comes
    When the app reads "HELLO" through a tunnel opened over HTTP/2 from pinned.example.com
    Then the tunnel ends
