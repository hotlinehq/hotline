Feature: Reading the integration ID
  The integration ID is the user name in "Proxy-Authorization: Basic".
  The password is ignored for now.

  Background:
    Given integration talon-shop-a with an api for host api.talon.one

  Scenario: The ID picks the integration and its api
    When an app calls https://api.talon.one with "Proxy-Authorization: Basic" for "talon-shop-a:x"
    Then the integration ID is talon-shop-a
    And the api for api.talon.one is used, with its spec, TLS origination setting and vendorTimeout

  Scenario Outline: Accepted forms of the header
    When an app calls https://api.talon.one with "Proxy-Authorization: <scheme>" for "<credentials>"
    Then the integration ID is talon-shop-a

    Examples:
      | scheme | credentials    |
      | Basic  | talon-shop-a:x |
      | Basic  | talon-shop-a:  |
      | basic  | talon-shop-a:x |
      | BASIC  | talon-shop-a:x |

  Scenario: No ID sent
    When an app calls https://api.talon.one without Proxy-Authorization
    Then the not-passed settings apply
    And the record's ID is not-passed

  Scenario: Unknown ID
    An ID the config does not name is recorded as not-passed, as if none
    was sent: an app could otherwise make up IDs without end, each a new
    series in the proxy's numbers (Peter, 2026-11-17).
    When an app calls https://api.talon.one with the ID "talon-shop-x"
    Then the not-passed settings apply
    And the record's ID is not-passed

  Scenario: Host outside the integration's apis
    When an app calls https://api.stripe.com with the ID "talon-shop-a"
    Then the not-passed settings apply
    And the record's ID is talon-shop-a

  Scenario Outline: Broken headers count as no ID
    When an app calls https://api.talon.one with <header>
    Then the not-passed settings apply
    And the record's ID is not-passed

    Examples:
      | header                                         |
      | "Proxy-Authorization: Bearer abc"              |
      | "Proxy-Authorization: Basic !!not-base64!!"    |
      | Basic credentials with no colon                |
      | Basic credentials with an empty user name      |
      | two Proxy-Authorization headers                |

  Scenario: The header never leaks
    When an app calls with a broken Proxy-Authorization header
    Then its value appears in no log, error or record
