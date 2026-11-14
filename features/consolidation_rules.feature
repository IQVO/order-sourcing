@rules
Feature: Consolidation at release: who may merge, who may not, and what a dissolution does
  ConsolidationAtRelease and ConsolidationDissolution of warehouse-docs
  network/docs/contexts/order-sourcing (aggregate canvas, rules c1 identity, c2 node, c3 window
  overlap, c4 stock, c5 caps; use cases ReleaseToNode, HandleNodeRefusal, ApplyRevisedPromise):
  the cases that keep a group OUT of a merge, the cap, and the facts that dissolve or keep a merge.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  # --- c5: the cap of the policy ------------------------------------------------------------------

  Scenario: A cap of 2 takes one follower and leaves the third group queued
    Given the operator allows 2 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 1 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 1 units of "cat-1"
    And shopper "s1" places order "ord-3" with a CONSOLIDATED group of 1 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last release event lists 2 members and 2 units
    And the assignment of "ord-2" group 1 follows the carrier "ord-1" group 1
    And the assignment of "ord-3" group 1 is "QUEUED"

  Scenario: A cap of 1 means consolidation is off
    Given the operator allows 1 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

  Scenario: A later policy that leaves the cap out switches consolidation off again
    Given the operator allows 4 assignments per merged release
    And the operator publishes a sourcing policy of 0 cents, 10 percent and 0 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

  # --- c1: identity ---------------------------------------------------------------------------------

  Scenario: Another shopper's group never merges
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s2" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

  Scenario Outline: A group without a usable consolidation key is queued normally and never merges
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And order "ord-2" is placed with a CONSOLIDATED group of 3 units of "cat-1" under the consolidation key "<key>"
    And the assignment of "ord-2" group 1 is "QUEUED"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

    Examples:
      | key        |
      |            |
      | not-a-hash |

  Scenario: The same shopper's group for another postal prefix never merges
    Given the operator allows 4 assignments per merged release
    And the postal prefix "30302" is also served by the delivery station "ds-1"
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1" for the postal prefix "30302"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"

  # --- c2: node ---------------------------------------------------------------------------------------

  Scenario: A group that already refused the carrier's node is not absorbed into its release
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-2" group 1
    And the node rejects release 1 of "ord-2" group 1
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-2" group 1 is "QUEUED"
    And the assignment of "ord-2" group 1 excludes the node "fc-a"

  # --- c3: window overlap -----------------------------------------------------------------------------

  Scenario Outline: Only groups whose promised windows overlap share a release
    Given the operator allows 4 assignments per merged release
    And the delivery station "ds-1" has a second wave on day two
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1" and a window from <from1> to <to1> hours
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1" and a window from <from2> to <to2> hours
    When I release the assignment of "ord-1" group 1
    Then the command succeeds
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the assignment of "ord-2" group 1 is "<state>"

    Examples:
      | from1 | to1 | from2 | to2 | state    |
      | 10    | 30  | 20    | 60  | RELEASED |
      | 10    | 30  | 40    | 60  | QUEUED   |
      | 30    | 60  | 10    | 30  | QUEUED   |

  # --- c4: stock --------------------------------------------------------------------------------------

  Scenario Outline: The summed quantity must be covered by one owner, one unit over is not
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of <units1> units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of <units2> units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the command succeeds
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the assignment of "ord-2" group 1 is "<state>"

    Examples:
      | units1 | units2 | state    |
      | 5      | 5      | RELEASED |
      | 6      | 5      | QUEUED   |

  # --- dissolution and what stays merged -------------------------------------------------------------

  Scenario: An expired carrier release dissolves the merge and says why
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node lets release 1 of "ord-1" group 1 expire
    Then the assignment of "ord-1" group 1 is "QUEUED"
    And the assignment of "ord-2" group 1 is "QUEUED"
    And the assignment of "ord-2" group 1 follows no carrier
    And the last "AssignmentsDeconsolidated" event has "Reason" "PURCHASE_ORDER_EXPIRED"
    And the last "AssignmentsDeconsolidated" event lists 2 "MemberAssignmentIDs"

  Scenario: A revision that keeps the merged delivery inside the window leaves the merge intact
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When promise "prm-ord-2-1" is revised to revision 1 with the window from 10 to 70 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-2" group 1 follows the carrier "ord-1" group 1
    And the assignment of "ord-1" group 1 is "RELEASED"

  Scenario: After the lock nothing dissolves, even when a revision breaks the window
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node accepts release 1 of "ord-1" group 1
    When promise "prm-ord-2-1" is revised to revision 1 with the deadline in 10 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-2" group 1 is "LOCKED"
    And the assignment of "ord-2" group 1 follows the carrier "ord-1" group 1

  Scenario: The vendor only knows the carrier, so an outcome addressed to a follower is stale
    Given the operator allows 4 assignments per merged release
    And shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    And shopper "s1" places order "ord-2" with a CONSOLIDATED group of 3 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node accepts release 1 of "ord-2" group 1
    Then the command is refused with "ErrReleaseNotPending"
    When the node rejects release 1 of "ord-2" group 1
    Then the command is refused with "ErrReleaseNotPending"
    And the last command published nothing
    And the assignment of "ord-2" group 1 is "RELEASED"
    And the assignment of "ord-1" group 1 is "RELEASED"
