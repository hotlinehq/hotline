Feature: A relayed tunnel stops each direction on its own
  A tunnel the proxy relays untouched carries two directions: from the app
  to the vendor, and back. When one side stops sending, the proxy tells the
  other side that nothing more comes from there, and keeps the other
  direction open. So a reply still on its way, or bytes the app still
  sends, are never lost (review, Peter 2026-11-12). This holds for ordinary
  tunnels over HTTP/1.1 and HTTP/2, and for captured tunnels.

  Background:
    Given an interception issuer named customer in issuers, which listen.connect picks
    And listen.capture is a socket file, whose issuer is customer too
    And listen.connect lists pinned.example.com in passthrough
    And listen.capture lists pinned.example.com in passthrough

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
      | a tunnel opened over HTTP/2   |
      | a captured tunnel             |
