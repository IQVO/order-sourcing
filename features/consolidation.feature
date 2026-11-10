Feature: Carrier and follower consolidation at release
  Groups of one shopper (the same consolidation key, all CONSOLIDATED) that are QUEUED when the
  first of them reaches its release point leave as ONE purchase order (ADR 0015). The first group is
  the carrier, the others its followers. Every member keeps its own events, locks with the carrier
  and is delivered with it. A cancel of a member is refused while the merge is live. It is all off
  until an operator publishes consolidationMaxAssignments of 2 or more.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  Scenario: Two groups of one shopper become one release, one acceptance and one delivery
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the command succeeds
    And the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided, AssignmentsConsolidated, FulfillmentSourcingDecided"
    And the last release event lists 2 members and 5 units
    And the assignment of "ord-2" group 1 follows the carrier "ord-1" group 1
    When the node accepts release 1 of "ord-1" group 1
    Then the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-2" group 1 is "LOCKED"
    When the vendor ships the packages "T1" of "ord-1" group 1
    Then the assignment of "ord-2" group 1 is "SHIPPED"
    When package "T1" of "ord-1" group 1 is delivered
    Then the assignment of "ord-1" group 1 is "DELIVERED"
    And the assignment of "ord-2" group 1 is "DELIVERED"

  # A SAME_DAY group is released when it is queued, so it never waits to be a carrier.
  Scenario Outline: Only CONSOLIDATED groups merge
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a <tier> group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

    Examples:
      | tier     |
      | NEXT_DAY |
      | TWO_DAY  |
      | STANDARD |

  Scenario: Without a policy nothing merges
    Given shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

  Scenario: A cancel of a merged member is refused until the carrier locks
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the customer cancels group 1 of order "ord-2"
    Then the last command published "FulfillmentCancelRefused"
    And the assignment of "ord-2" group 1 is "RELEASED"
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelRefused"
    And the assignment of "ord-1" group 1 is "RELEASED"

  Scenario: A refused carrier sends every member back to the queue and the cancel is allowed again
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node rejects release 1 of "ord-1" group 1
    Then the assignment of "ord-1" group 1 is "QUEUED"
    And the assignment of "ord-2" group 1 is "QUEUED"
    And the assignment of "ord-2" group 1 excludes the node "fc-a"
    When the customer cancels group 1 of order "ord-2"
    Then the last command published "FulfillmentCancelled"

  Scenario: A revision that breaks the merged window withdraws the release and the survivor is released alone
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When promise "prm-ord-2-1" is revised to revision 1 with the deadline in 10 hours
    Then the last command published "FulfillmentConstraintRevised, FulfillmentReleaseWithdrawn, AssignmentsDeconsolidated, FulfillmentUnsourceable"
    And the assignment of "ord-2" group 1 is "UNSOURCEABLE"
    And the assignment of "ord-1" group 1 is "QUEUED"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
