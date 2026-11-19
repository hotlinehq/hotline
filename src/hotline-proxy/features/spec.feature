Feature: Reading the vendor spec
  Our copy of a vendor's OpenAPI spec gives the route templates, the
  x-hotline-redact marks and the secrets named in its security section.
  What calls are checked against is in checks.feature.

  Scenario: Route template for a path with a value
    Given the Talon.One spec
    When a call is PUT /v2/customer_sessions/sess-8812
    Then the route template is /v2/customer_sessions/{customerSessionId}

  Scenario: Route template for a path without values
    Given the Stripe spec
    When a call is GET /v1/payment_intents
    Then the route template is /v1/payment_intents

  Scenario: The method must match too
    Given the Talon.One spec
    When a call is GET /v2/customer_sessions/sess-8812
    Then there is no route, because the spec only lists PUT

  Scenario: A path the spec does not know
    Given the Talon.One spec
    When a call is PUT /v2/unknown
    Then there is no route

  Scenario: One value does not swallow several segments
    Given the Talon.One spec
    When a call is PUT /v2/customer_sessions/a/b
    Then there is no route

  Scenario: A percent-encoded value is one value
    Given the Talon.One spec
    When a call is PUT /v2/customer_sessions/a%2Fb
    Then the route template is /v2/customer_sessions/{customerSessionId}

  Scenario: A fixed route wins over a templated one
    Given a spec with the routes /v1/items/{id} and /v1/items/search
    When a call is GET /v1/items/search
    Then the route template is /v1/items/search

  Scenario Outline: A trailing slash makes no difference
    Given a spec with the route <route>
    When a call is GET <path>
    Then the route template is <route>

    Examples:
      | route      | path       |
      | /v1/items/ | /v1/items  |
      | /v1/items  | /v1/items/ |

  Scenario: Base path in the servers address
    Given a spec whose servers address is https://api.x.com/v1 with the route /items
    When a call is GET /v1/items
    Then the route template is /v1/items

  Scenario Outline: Reading the marks
    Given a spec where <parameter> is <marked>
    When the spec is read
    Then <result>

    Examples:
      | parameter                  | marked                        | result                                  |
      | a path parameter           | marked x-hotline-redact false | its value is to be kept                 |
      | a query parameter          | marked x-hotline-redact false | its value is to be kept                 |
      | a header parameter         | marked x-hotline-redact true  | its name is kept and its value redacted |
      | a header parameter         | marked x-hotline-redact false | its value is to be kept                 |
      | any parameter              | not marked                    | it counts as not marked                 |
      | a declared response header | marked x-hotline-redact false | its value is to be kept                 |

  Scenario: A method's own parameter entry wins
    Given a spec that lists the query parameter page for the whole route as not marked
    And lists page again for GET, marked x-hotline-redact false
    When the spec is read for GET
    Then page is to be kept

  Scenario: Header names match in any case
    Given a spec with the header X-Debug-Token marked x-hotline-redact true
    When a call sends x-debug-token
    Then the mark applies

  Scenario Outline: Secrets from the security section
    Given a spec whose security section has <scheme>
    When the spec is read
    Then <secret> is a secret

    Examples:
      | scheme                                                         | secret                   |
      | an http bearer scheme                                          | the Authorization header |
      | an http basic scheme                                           | the Authorization header |
      | an apiKey scheme in the Authorization header, as Talon.One has | the Authorization header |
      | an apiKey scheme in the query named key                        | the query parameter key  |
      | an apiKey scheme in a cookie named sid                         | the cookie sid           |

  Scenario: Secrets from one operation are added
    Given a spec where one operation has its own security section with an apiKey header X-Op-Key
    When the spec is read
    Then X-Op-Key is a secret as well as the global ones
