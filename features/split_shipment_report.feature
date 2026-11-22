Feature: The split-shipment rate of the sourcing-mix report
  Per UTC day of an order's first sourcing decision, the sourcing-mix report says how many sourced
  orders there are, how many of them left as two or more shipment groups, and how the shipment
  groups per order are distributed over the buckets 1, 2, 3 and 4+ (ADR 0018). A follower merged
  into its carrier shares the carrier's shipment. The scenarios release for real, fold what the
  service published into the analytics projection and read the real reports router.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  Scenario: An order released as two groups is a split decision, an order of one group is not
    Given order "ord-1" is placed with 2 NEXT_DAY groups of 1 units of "cat-1"
    And order "ord-2" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release every queued assignment
    When the analytics projection folds what the service published
    And I read the sourcing-mix report of 2026-10-22
    Then the day 2026-10-22 has 2 split-shipment decisions of which 1 is split
    And the day 2026-10-22 has the split share 0.5
    And the day 2026-10-22 has the groups per decision 1:1, 2:1, 3:0, 4+:0

  Scenario: Five groups fall in the 4+ bucket
    Given order "ord-1" is placed with 5 NEXT_DAY groups of 1 units of "cat-1"
    And order "ord-2" is placed with 3 NEXT_DAY groups of 1 units of "cat-1"
    And I release every queued assignment
    When the analytics projection folds what the service published
    And I read the sourcing-mix report of 2026-10-22
    Then the day 2026-10-22 has 2 split-shipment decisions of which 2 are split
    And the day 2026-10-22 has the split share 1
    And the day 2026-10-22 has the groups per decision 1:0, 2:0, 3:1, 4+:1

  Scenario: Two groups of one order merged into one carrier ship together and are not split
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with 2 CONSOLIDATED groups of 1 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    And the analytics projection folds what the service published
    And I read the sourcing-mix report of 2026-10-22
    Then the day 2026-10-22 has 1 split-shipment decisions of which 0 are split
    And the day 2026-10-22 has the split share 0
    And the day 2026-10-22 has the groups per decision 1:1, 2:0, 3:0, 4+:0

  Scenario: A group nobody can serve gives a day with no split-shipment decision
    Given order "ord-1" is placed with a NEXT_DAY group of 1 units of "cat-unknown"
    When the analytics projection folds what the service published
    And I read the sourcing-mix report of 2026-10-22
    Then the day 2026-10-22 has 0 split-shipment decisions of which 0 are split
    And the day 2026-10-22 has no split share
    And the day 2026-10-22 has the groups per decision 1:0, 2:0, 3:0, 4+:0

  Scenario: An empty day has no row and an empty range has no day
    Given order "ord-1" is placed with a NEXT_DAY group of 1 units of "cat-1"
    And I release every queued assignment
    When the analytics projection folds what the service published
    And I read the sourcing-mix report of 2026-10-23
    Then the report has no day

  Scenario: Nothing was sourced at all
    When I read the sourcing-mix report of 2026-10-22
    Then the report has no day
