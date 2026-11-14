Feature: The sourcing policy document: ranges, whole-document publish, defaults and the budget arithmetic
  SourcingPolicy of warehouse-docs network/docs/contexts/order-sourcing (aggregate canvas "SourcingPolicy",
  use case PublishSourcingPolicy, policy RegionalBudget): every field inside its range and refused one
  step outside, a PUT is the WHOLE document, the defaults are version 0, and the in-region budget is the
  cheapest effective cost plus floor(cheapest x percent / 100).

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  Scenario Outline: Every field accepts its maximum and refuses one step outside its range
    When the operator publishes a sourcing policy of <cents> cents, <percent> percent and <cap> assignments per merged release
    Then the command <result>
    And the current sourcing policy is version <version>

    Examples:
      | cents  | percent | cap | result                 | version |
      | 100000 | 0       | 0   | succeeds               | 1       |
      | 100001 | 0       | 0   | is rejected as invalid | 0       |
      | -1     | 0       | 0   | is rejected as invalid | 0       |
      | 0      | 200     | 0   | succeeds               | 1       |
      | 0      | 201     | 0   | is rejected as invalid | 0       |
      | 0      | -1      | 0   | is rejected as invalid | 0       |
      | 0      | 0       | 10  | succeeds               | 1       |
      | 0      | 0       | 11  | is rejected as invalid | 0       |
      | 0      | 0       | -1  | is rejected as invalid | 0       |

  Scenario: Before anything is published the policy is version 0 with every lever off
    When the planner reads the sourcing policy
    Then the response status is 200
    And the response field "version" is "0"
    And the response field "crossRegionSurchargeCents" is "0"
    And the response field "maxCostDeviationPercent" is "0"
    And the response field "consolidationMaxAssignments" is "0"
    And the response has no field "publishedAt"

  Scenario: A PUT is the whole document, so an omitted field is 0
    When the planner publishes the sourcing policy with the JSON body {"crossRegionSurchargeCents":250}
    Then the response status is 200
    And the response field "version" is "1"
    And the response field "crossRegionSurchargeCents" is "250"
    And the response field "maxCostDeviationPercent" is "0"
    And the response field "consolidationMaxAssignments" is "0"
    And the last command published "SourcingPolicyPublished"
    When the planner reads the sourcing policy
    Then the response field "crossRegionSurchargeCents" is "250"

  Scenario: An empty body is refused and publishes nothing
    When the planner publishes the sourcing policy with an empty body
    Then the response status is 400
    And the response problem type is "malformed-request"
    And the current sourcing policy is version 0

  Scenario: A field outside its range is a validation problem and publishes nothing
    When the planner publishes the sourcing policy with the JSON body {"maxCostDeviationPercent":201}
    Then the response status is 422
    And the response problem type is "validation-failed"
    And the current sourcing policy is version 0

  Scenario Outline: The in-region budget is the cheapest cost plus the percentage rounded down
    Given the pickup lane of "fc-b" costs 999 cents
    And the pickup lane of "fc-a" costs <inRegion> cents
    And the operator publishes a sourcing policy of 0 cents, 10 percent and 0 assignments per merged release
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "<node>"

    Examples:
      | inRegion | node |
      | 1098     | fc-a |
      | 1099     | fc-b |

  Scenario: A published budget of 0 percent leaves the cheapest lane first
    Given the pickup lane of "fc-b" costs 1000 cents
    And the pickup lane of "fc-a" costs 1100 cents
    And the operator publishes a sourcing policy of 0 cents, 0 percent and 0 assignments per merged release
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
