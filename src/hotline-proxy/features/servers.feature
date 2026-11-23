# Requirements and decisions: docs/decisions/spec-checks.md (second sweep, 2026-11-23).
Feature: Every server's base path
  A server's base path is the path part of its address, such as /v1 in
  https://api.x.com/v1, put in front of every route. The host is never
  used, as the config names the hosts. Before 2026-11-23 only the first
  top-level server was read, so a call on another server's base path found
  no route. Now every server is read, and servers on a path or an
  operation take over from the ones above, as OpenAPI says (Peter,
  2026-11-23, Q2).

  Scenario Outline: Each top-level server gives the routes its base path
    Given a spec with the servers https://api.x.com/v1 and https://api.x.com/beta, and the route /items
    When a call is GET <path>
    Then the route template is <path>

    Examples:
      | path        |
      | /v1/items   |
      | /beta/items |

  Scenario: Servers with the same base path give one route
    The Kraken spec lists ws.kraken.com and ws-auth.kraken.com, both with no
    base path.
    Given a spec with the servers https://ws.x.com and https://ws-auth.x.com, and the route /v2
    When a call is GET /v2
    Then the route template is /v2

  Scenario Outline: A path's own servers take over from the top-level ones
    Given a spec with the server https://api.x.com/v1, whose path /files has the server https://upload.x.com/upload/v1
    When a call is GET <path>
    Then <outcome>

    Examples:
      | path             | outcome                                |
      | /upload/v1/files | the route template is /upload/v1/files |
      | /v1/files        | there is no route                      |

  Scenario Outline: An operation's own servers take over from its path's
    Given a spec whose path /files has the server https://api.x.com/a, and whose POST on it has the server https://api.x.com/b
    When a call is <call>
    Then <outcome>

    Examples:
      | call            | outcome                       |
      | GET /a/files    | the route template is /a/files |
      | POST /b/files   | the route template is /b/files |
      | POST /a/files   | there is no route              |

  Scenario: A method under additionalOperations may have its own servers too
    Given a spec whose path /files has the server https://api.x.com/a, and whose COPY on it has the server https://api.x.com/b
    When a call is COPY /b/files
    Then the route template is /b/files
