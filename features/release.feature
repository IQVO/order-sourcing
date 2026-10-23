Feature: Releasing, refusing and locking an assignment
  A release goes to one node at a time. A refusal excludes that node and re-queues, or
  declares the group unsourceable. Acceptance locks the assignment: the node never changes
  afterwards (ADR 0005).

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"

  Scenario: Releasing sends the group to the candidate node
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode"
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the assignment of "ord-1" group 1 has a PENDING release number 1 to "fc-a"

  Scenario: A released assignment cannot be re-optimised
    Given I release the assignment of "ord-1" group 1
    When I re-optimise the assignment of "ord-1" group 1
    Then the command is refused with "ErrReleaseInFlight"

  Scenario: A rejection excludes the node and re-queues on the next best one
    Given I release the assignment of "ord-1" group 1
    When the node rejects release 1
    Then the last command published "FulfillmentReassigned"
    And the assignment of "ord-1" group 1 is "QUEUED"
    And the assignment of "ord-1" group 1 has the candidate node "fc-b"
    And the assignment of "ord-1" group 1 excludes the node "fc-a"

  Scenario: An expiry is handled like a rejection
    Given I release the assignment of "ord-1" group 1
    When the node lets release 1 expire
    Then the assignment of "ord-1" group 1 excludes the node "fc-a"

  Scenario: When every node has refused the group is unsourceable
    Given I release the assignment of "ord-1" group 1
    And the node rejects release 1
    And I release the assignment of "ord-1" group 1
    When the node rejects release 2
    Then the last command published "FulfillmentUnsourceable"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"

  Scenario: A late outcome of an old release changes nothing
    Given I release the assignment of "ord-1" group 1
    And the node rejects release 1
    When the node rejects release 1
    Then the command is refused with "ErrReleaseNotPending"

  Scenario: Acceptance locks the assignment
    Given I release the assignment of "ord-1" group 1
    When the node accepts release 1
    Then the last command published "FulfillmentLocked"
    And the assignment of "ord-1" group 1 is "LOCKED"

  Scenario: A locked assignment cannot be moved
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When I re-optimise the assignment of "ord-1" group 1
    Then the command is refused with "ErrAssignmentLocked"
