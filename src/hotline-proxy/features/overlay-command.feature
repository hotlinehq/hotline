@e2e
Feature: Making an overlay, and overlays in validate
  hotline-proxy overlay writes an overlay (version 1.1) to standard output
  that turns the --from spec into the --to spec, for whoever writes an
  api's overlays. Before writing, it applies the overlay to --from and
  checks that the result is --to. It needs no config and starts no proxy
  (Peter, 2026-10-30). validate lists the warnings overlays give, as run
  does.

  Background:
    Given the file from.yaml:
      """
      openapi: 3.2.0
      info: { title: Shop, version: "1" }
      paths:
        /orders:
          get:
            parameters:
              - { name: limit, in: query, schema: { type: integer, maximum: 100 } }
            responses:
              "200": { description: Orders }
      """

  Scenario: An overlay made from two specs turns one into the other
    Given the file to.yaml:
      """
      openapi: 3.2.0
      info: { title: Shop, version: "1" }
      paths:
        /orders:
          get:
            parameters:
              - { name: limit, in: query, schema: { type: integer, maximum: 10 } }
            responses:
              "200": { description: Orders }
        /refunds:
          get:
            responses:
              "200": { description: Refunds }
      """
    When hotline-proxy is run with "overlay --from from.yaml --to to.yaml --title Invoices"
    Then it exits with code 0
    And standard output starts with "overlay: 1.1.0"
    And standard output says "title: Invoices"
    And standard output does not say "x-speakeasy"
    When standard output is saved as made.overlay.yaml
    And a config file whose api has the spec from.yaml and the overlay made.overlay.yaml
    And hotline-proxy is run with "validate --config hotline-proxy.yaml"
    Then it exits with code 0
    And standard error says "config is valid"
    And standard error does not say "matches nothing"

  Scenario: Two specs that are the same give no overlay
    Given the file to.yaml holds from.yaml with its keys in another order
    When hotline-proxy is run with "overlay --from from.yaml --to to.yaml"
    Then it exits with code 1
    And standard error says "the two specs are the same"
    And standard output is empty

  Scenario: An overlay that would not give the --to spec is not written
    A value changed to null is one change an overlay cannot make.
    Given the file to.yaml holds from.yaml with the title changed to null
    When hotline-proxy is run with "overlay --from from.yaml --to to.yaml"
    Then it exits with code 1
    And standard error says "does not turn"
    And standard output is empty

  Scenario Outline: Files the overlay command cannot use
    Given the file broken.yaml:
      """
      openapi: [3.2.0
      """
    When hotline-proxy is run with "overlay <arguments>"
    Then it exits with code 1
    And standard error says "<message>"
    And standard output is empty

    Examples:
      | arguments                          | message                 |
      | --from nowhere.yaml --to from.yaml | nowhere.yaml cannot be read |
      | --from from.yaml --to nowhere.yaml | nowhere.yaml cannot be read |
      | --from broken.yaml --to from.yaml  | broken.yaml is not YAML or JSON |
      | --from from.yaml --to broken.yaml  | broken.yaml is not YAML or JSON |

  Scenario Outline: A wrong overlay command line is refused with exit code 2
    When hotline-proxy is run with "overlay <arguments>"
    Then it exits with code 2
    And standard error says "<message>"

    Examples:
      | arguments                                  | message                        |
      |                                            | --from and --to are required   |
      | --from from.yaml                           | --from and --to are required   |
      | --from from.yaml --to from.yaml extra      | unexpected argument "extra"    |
      | --colour                                   | flag provided but not defined  |

  Scenario: Help for the overlay command is printed on request
    When hotline-proxy is run with "overlay --help"
    Then it exits with code 0
    And standard error says "-title"

  Scenario: validate lists the warnings of an overlay
    Given the file stale.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Stale, version: "1" }
      actions:
        - target: $.components.schemas.Gone
          remove: true
      """
    And a config file whose api has the spec from.yaml and the overlay stale.overlay.yaml
    When hotline-proxy is run with "validate --config hotline-proxy.yaml"
    Then it exits with code 0
    And standard error says "stale.overlay.yaml, action 1: target $.components.schemas.Gone matches nothing"
    And standard error says "config is valid"
