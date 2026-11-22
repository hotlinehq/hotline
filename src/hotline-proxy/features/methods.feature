# Requirements and decisions: docs/decisions/spec-checks.md (M3, 2026-11-22).
Feature: The QUERY method and other methods
  OpenAPI 3.2 adds the QUERY method, an HTTP method that only reads, like
  GET, but whose call carries a body saying what to look up. A path lists it
  under query, next to get and post. Any other method goes under
  additionalOperations, keyed by its name written exactly as it is sent
  (Peter, 2026-11-22, M3).

  Rule: Each of these methods is a route

    Scenario Outline: The method finds its route
      Given the methods spec
      When a call is <method> <path>
      Then the route template is <template>

      Examples:
        | method | path        | template      |
        | QUERY  | /v1/search  | /v1/search    |
        | COPY   | /v1/pets/7  | /v1/pets/{id} |
        | GET    | /v1/pets/7  | /v1/pets/{id} |

    Scenario: A method name must match in case
      Method names are case sensitive in HTTP, so COPY in the spec is not
      a copy call.
      Given the methods spec
      When a call is copy /v1/pets/7
      Then there is no route

    Scenario: A path without the method has no route
      Given the methods spec
      When a call is QUERY /v1/pets/7
      Then there is no route

  Rule: A QUERY call is checked like any other

    Scenario: A body that matches has no results
      Given the methods spec
      When a call is QUERY /v1/search with the json body {"filter":"cats"}
      Then the record has no request results

    Scenario: A body that breaks the spec is reported
      Given the methods spec
      When a call is QUERY /v1/search with the json body {}
      Then the request results are body $.filter required

    Scenario: A COPY call's parameters are checked
      Given the methods spec
      When a call is COPY /v1/pets/abc
      Then the request results are path id type integer

  Rule: A spec that lists a method wrongly is refused

    Scenario Outline: A method with its own field may not be listed again
      OpenAPI 3.2 forbids it: the method's own field is the only place for it.
      Given a spec whose path /v1/pets lists <key> under additionalOperations
      When the proxy loads the config
      Then the config is refused with a message naming "/v1/pets" and "<key>"

      Examples:
        | key   |
        | POST  |
        | post  |
        | QUERY |
        | Get   |

    Scenario Outline: A key that is not a method name is refused
      Given a spec whose path /v1/pets lists "<key>" under additionalOperations
      When the proxy loads the config
      Then the config is refused with a message naming "/v1/pets" and "not a method name"

      Examples:
        | key     |
        | MY COPY |
        | COPY:   |
        |         |
