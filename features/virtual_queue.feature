Feature: The virtual queue timer and the booking facts
  The sweep releases QUEUED assignments whose release point has passed (ADR 0002 A18)
  and re-optimises the others. A booking the linehaul moved (BookingMoved, G14) is
  followed by a locked group; a failed booking changes nothing here (A19) — it is only
  recorded as processed, so a redelivery stays a no-op.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"

  Scenario: The sweep releases a group whose release point has passed
    Given the clock is advanced by 3 hours
    When the sweep runs
    Then the sweep released 1 assignment
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the last command published "FulfillmentReleasedToNode"

  Scenario: Nothing is released before the release point
    When the sweep runs
    Then the sweep released 0 assignments
    And the sweep reoptimised 1 assignment
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "QUEUED"

  Scenario: A locked group follows a booking that moved
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the booking of "ord-1" group 1 moves from departure "pk-a" to "pk-a2"
    Then the command succeeds
    And the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-1" group 1 follows the pickup departure "pk-a2"

  Scenario: A booking move from another departure changes nothing
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the booking of "ord-1" group 1 moves from departure "pk-other" to "pk-a2"
    Then the command succeeds
    And the assignment of "ord-1" group 1 follows the pickup departure "pk-a"

  Scenario: A failed booking is only recorded, once
    When event "evt-bf-1" is recorded as processed
    Then the command succeeds
    And the last command published nothing
    When event "evt-bf-1" is recorded as processed again
    Then the command succeeds
    And the last command published nothing
    And there is exactly 1 assignment
