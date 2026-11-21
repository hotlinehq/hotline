# Requirements and answers are in docs/proxy-overlays.md.
Feature: Overlays on an api's spec
  An api can list overlays next to its spec: files in the OpenAPI Overlay
  format, version 1.1. An overlay is a list of actions, and each action
  says "at this spot in the spec, change this"; the spot is found with
  JSONPath (RFC 9535). The proxy applies the overlays when it loads the
  config, in the order listed, and then treats the result like any spec.
  Calls are checked only against that result (Peter, 2026-10-30).

  Background:
    Given the overlay limit-10.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Ten at most, version: "1" }
      actions:
        - target: $.paths['/orders'].get.parameters[?@.name == 'limit'].schema
          update: { maximum: 10 }
      """

  Scenario: An overlay changes what is checked
    Given the shop spec with the overlay limit-10.overlay.yaml
    When the app sends GET /orders?limit=50
    Then the request results are query limit maximum 10

  Scenario: Another api on the same spec keeps the spec as it is
    Given the shop spec with the overlay limit-10.overlay.yaml
    And the integration talon-shop-b with the same shop spec and no overlay
    When talon-shop-b sends GET /orders?limit=50
    Then the record has no request results

  Scenario: Overlays are applied in the order listed
    Given the overlay limit-20.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Twenty at most, version: "1" }
      actions:
        - target: $.paths['/orders'].get.parameters[?@.name == 'limit'].schema
          update: { maximum: 20 }
      """
    And the shop spec with the overlays limit-10.overlay.yaml, limit-20.overlay.yaml
    When the app sends GET /orders?limit=15
    Then the record has no request results

  Scenario: An overlay can add a route
    Given the overlay refunds.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Refunds, version: "1" }
      actions:
        - target: $.paths
          update:
            /refunds:
              get:
                responses: { "200": { description: Refunds } }
      """
    And the shop spec with the overlay refunds.overlay.yaml
    When the app sends GET /refunds
    Then url.template is /refunds

  Scenario: An overlay can remove a route
    Given the overlay no-order.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: No single order, version: "1" }
      actions:
        - target: $.paths['/orders/{orderId}']
          remove: true
      """
    And the shop spec with the overlay no-order.overlay.yaml
    When the app sends GET /orders/0b6f4c1e-3f2a-4a7e-9c55-2f1b8e0d9a11
    Then there is no route

  Scenario: An overlay can copy a part of the spec
    The new route /drafts gets the same request as POST /orders.
    Given the overlay drafts.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Drafts, version: "1" }
      actions:
        - target: $.paths
          update:
            /drafts:
              post: {}
        - target: $.paths['/drafts'].post
          copy: $.paths['/orders'].post
      """
    And the shop spec with the overlay drafts.overlay.yaml
    When the app sends POST /drafts with "Idempotency-Key: k1" and the json body {"item":"A1"}
    Then the request results are body $.quantity required

  Scenario: The overlay's extends field is ignored
    The config says which spec an overlay goes on.
    Given the overlay elsewhere.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Elsewhere, version: "1" }
      extends: ./some-other.openapi.yaml
      actions:
        - target: $.paths['/orders'].get.parameters[?@.name == 'limit'].schema
          update: { maximum: 10 }
      """
    And the shop spec with the overlay elsewhere.overlay.yaml
    When the app sends GET /orders?limit=50
    Then the request results are query limit maximum 10

  Scenario Outline: An action whose target matches nothing is a warning
    The action does nothing, as the format says, and the proxy starts. The
    overlay is probably out of date, so a warning says so (Peter, 2026-10-30).
    Given the overlay stale.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Stale, version: "1" }
      actions:
        - target: $.paths['/orders'].get.parameters[?@.name == 'limit'].schema
          update: { maximum: 10 }
        - target: $.components.schemas.Gone
          <action>
      """
    And the shop spec with the overlay stale.overlay.yaml
    When the app sends GET /orders?limit=50
    Then the request results are query limit maximum 10
    And the proxy logged a warning with "api.shop.test", "stale.overlay.yaml", "action 2" and "matches nothing"

    Examples:
      | action                 |
      | update: { type: object } |
      | remove: true           |

  Scenario Outline: A broken overlay stops the proxy at start
    Given the overlay broken.overlay.yaml:
      """
      <overlay>
      """
    And the shop spec with the overlay broken.overlay.yaml
    When the proxy loads the config
    Then the config is refused with a message naming "api.shop.test", "broken.overlay.yaml" and "<problem>"

    Examples:
      | overlay                                                                                                              | problem                               |
      | overlay: [1.1.0                                                                                                      | is not an overlay document            |
      | overlay: 1.1.0                                                                                                       | is not an overlay document            |
      | { overlay: 1.0.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, update: {} } ] }                   | version 1.0.0 is not supported        |
      | { overlay: 2.0.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, update: {} } ] }                   | version 2.0.0 is not supported        |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: "$.paths[", update: {} } ] }                | action 1: target $.paths[ is not valid JSONPath |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: "$.paths[?(@.x in ['a'])]", update: {} } ] } | is not valid JSONPath                 |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, copy: $.components.schemas.Gone } ] } | action 1: copy $.components.schemas.Gone matches nothing |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, update: [ 1 ] } ] }                | action 1:                             |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, copy: "$.paths[" } ] }             | action 1: copy $.paths[ is not valid JSONPath |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: $.paths, copy: "$.paths.*" } ] }            | action 1: copy $.paths.* matches 6 spots, not one |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: $.openapi, update: 3.0.3 } ] }              | says openapi 3.0.3, but Hotline reads OpenAPI 3.2 only |
      | { overlay: 1.1.0, info: { title: T, version: "1" }, actions: [ { target: "$.paths['/orders'].post.requestBody.content['application/json'].schema", update: { $ref: "#/components/schemas/Gone" } } ] } | Gone |

  Scenario: An overlay can move a spec's version line to 3.2
    The version is checked after the overlays are applied, so the result
    is what counts (Peter, 2026-11-21).
    Given the overlay to-3.2.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: To 3.2, version: "1" }
      actions:
        - target: $
          update: { openapi: 3.2.0 }
      """
    And the shop spec with the overlays to-3.2.overlay.yaml, limit-10.overlay.yaml
    And the shop spec file says openapi 3.1.0
    When the app sends GET /orders?limit=50
    Then the request results are query limit maximum 10

  Scenario: A spec that is not YAML, with an overlay, stops the proxy at start
    Given the shop spec with the overlay limit-10.overlay.yaml
    And the shop spec file holds "openapi: [3.2.0"
    When the proxy loads the config
    Then the config is refused with a message naming "api.shop.test" and "the spec is not YAML or JSON"

  Scenario: An overlay file that cannot be read stops the proxy at start
    Given the shop spec with the overlay missing.overlay.yaml
    When the proxy loads the config
    Then the config is refused with a message naming "api.shop.test", "missing.overlay.yaml" and "cannot be read"

  Scenario: An integration cannot add overlays
    Overlays belong to the api only (Peter, 2026-10-30).
    Given the shop spec for talon-shop-a with "overlays: [./specs/limit-10.overlay.yaml]" on its entry for the api
    When the app sends GET /orders?limit=50
    Then the record has no request results
    And the proxy logged a warning with "overlays" and "is not a setting an integration may change"
