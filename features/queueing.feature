Feature: Queueing a placed shipment group
  A placed order becomes one virtual-queue assignment per shipment group. The group is
  UNSOURCEABLE at once when no node can serve it (model gap G26), and a repeated order
  event never queues twice.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  Scenario: A group with a candidate waits in the queue
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the command succeeds
    And the last command published "FulfillmentQueued"
    And the assignment of "ord-1" group 1 is "QUEUED"
    And the assignment of "ord-1" group 1 has the candidate node "fc-a"

  Scenario: A group nobody can serve is unsourceable immediately
    When order "ord-1" is placed with a NEXT_DAY group of 1 units of "cat-unknown"
    Then the command succeeds
    And the last command published "FulfillmentQueued, FulfillmentUnsourceable"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"

  Scenario: The same order event delivered twice queues once
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the same order event is delivered again
    Then the command succeeds
    And the last command published nothing
    And there is exactly 1 assignment

  Scenario: An unsourceable group can be cancelled
    Given order "ord-1" is placed with a NEXT_DAY group of 1 units of "cat-unknown"
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelled"
    And the assignment of "ord-1" group 1 is "CANCELLED"

  Scenario: Re-optimising with a stale version is refused
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When I re-optimise the assignment of "ord-1" group 1 expecting version 99
    Then the command is refused with "ErrStaleAssignmentVersion"

  Scenario: Re-optimising follows a change of capacity
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" has no stock of "cat-1" any more
    When I re-optimise the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReassigned"
    And the assignment of "ord-1" group 1 has the candidate node "fc-b"
