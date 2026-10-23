Feature: Revised promises
  A revised promise applies in every non-terminal state and publishes
  FulfillmentConstraintRevised (gap G13). A revision that is not above the applied one is
  ignored, and node exclusions reset when the revision changes.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"

  Scenario: A queued assignment takes the new deadline
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-1" group 1 has promise revision 1

  Scenario: A locked assignment takes the new deadline and keeps its node
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-1" group 1 has promise revision 1

  Scenario: A stale revision is ignored
    Given promise "prm-ord-1-1" is revised to revision 2 with the deadline in 60 hours
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 70 hours
    Then the last command published nothing
    And the assignment of "ord-1" group 1 has promise revision 2

  Scenario: Exclusions reset with a new revision
    Given I release the assignment of "ord-1" group 1
    And the node rejects release 1
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the assignment of "ord-1" group 1 excludes no node
