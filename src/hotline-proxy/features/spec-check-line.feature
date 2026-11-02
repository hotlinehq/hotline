# Agreed with Peter, 2026-11-02. Found by the load test: large replies at
# about 450 a second filled every processor core with spec checks, and
# every call then waited about a second.
Feature: Spec checks wait in a line capped by memory
  The proxy checks every request and reply against the spec, as far as it
  can. The checks run on one worker per processor core, apart from the
  calls, so a call never waits for its check. A check that finds every
  worker busy waits in a line. The line holds the body copies its checks
  need, and specChecks.queueMemory caps the memory they may take, 64 MiB
  unless set. When a new check does not fit, the oldest waiting checks give
  way: each is skipped as overloaded, and its call's record goes out with
  hotline.schema.skipped=overloaded. Bodies never go to disk.

  The tests run the proxy with a single worker, and keep it busy with the
  check of one large order, so the small calls after it must wait.

  Background:
    Given the shop spec
    And the vendor answers 201
    And the proxy checks with a single worker

  Scenario: Every check runs when the line has room
    When the app sends a large order, then 5 small orders
    Then all 6 records say hotline.schema.status=valid
    And every check's trace has a "wait for check worker" step under it

  Scenario: The oldest waiting checks give way
    Each small order's body takes a 16 KB buffer, so a 32 KiB line holds
    two of them.
    Given specChecks.queueMemory is 32KiB
    When the app sends a large order, then 8 small orders
    Then the first small order's record says hotline.schema.skipped=overloaded
    And the last small order's record says hotline.schema.status=valid
    And the "wait for check worker" step of each skipped check failed with error.type=overloaded
    And the skipped checks' "request spec check" steps are neither ok nor failed

  Scenario: A body larger than the whole line cannot wait
    Given specChecks.queueMemory is 16KiB
    When the app sends a large order, then a small order of 20 KB
    Then the last small order's record says hotline.schema.skipped=overloaded

  Scenario: The line's numbers are in the proxy's own telemetry
    Given a fake OTLP receiver for the proxy's own telemetry
    And specChecks.queueMemory is 32KiB
    When the app sends a large order, then 8 small orders
    Then the telemetry receiver gets hotline.spec_check.queue.memory.usage with unit "By" and a description
    And the telemetry receiver gets hotline.spec_check.queue.size with unit "{check}" and a description
    And the telemetry receiver gets hotline.spec_check.dropped with unit "{check}" and a description
    And hotline.spec_check.dropped counts the skipped checks

  Scenario Outline: The cap is written in bytes
    Given specChecks.queueMemory is <value>
    Then the proxy <starts>

    Examples:
      | value  | starts                                                                   |
      | 64MiB  | starts                                                                   |
      | 512KiB | starts                                                                   |
      | 1GiB   | starts                                                                   |
      | 1000   | starts                                                                   |
      | 0      | refuses the config: specChecks.queueMemory must be at least 1 byte       |
      | lots   | refuses the config: specChecks.queueMemory must be a size such as 64MiB  |

  Scenario: A smaller cap applies on reload
    Given specChecks.queueMemory is 64MiB
    And the proxy starts
    When specChecks.queueMemory is changed to 16KiB and the proxy is told to reload
    And the app sends a large order, then a small order of 20 KB
    Then the last small order's record says hotline.schema.skipped=overloaded
    And the proxy logs that the config was reloaded

  Scenario: Checks still waiting when the time to stop runs out are skipped
    When the app sends a large order, then 3 small orders
    And the proxy is stopped with no time left
    Then the proxy logs that it skipped the spec checks still waiting
