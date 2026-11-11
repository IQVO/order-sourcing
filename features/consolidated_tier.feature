Feature: The CONSOLIDATED delivery day
  A CONSOLIDATED group promises a delivery window the customer chose. The search accepts only a
  delivery wave that ends no earlier than the window start and, among equally cheap options, holds
  the latest pickup that still reaches it (ADR 0016). The vendor and the linehaul never see the
  fifth tier: a release carries STANDARD. Every other tier is searched as before.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And the delivery station "ds-1" has a second wave on day two

  Scenario: A window that starts on day two lands on the day-two wave
    When shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1" and a window starting in 30 hours
    Then the assignment of "ord-1" group 1 targets the delivery wave "w2"

  Scenario: Without a window start the earliest wave stays
    When shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 targets the delivery wave "w1"

  Scenario Outline: Another tier ignores the window start
    When shopper "s1" places order "ord-1" with a <tier> group of 2 units of "cat-1" and a window starting in 30 hours
    Then the assignment of "ord-1" group 1 targets the delivery wave "w1"

    Examples:
      | tier     |
      | NEXT_DAY |
      | TWO_DAY  |
      | STANDARD |

  Scenario: A day with no wave leaves the group unsourceable
    When shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1" and a window starting in 50 hours
    Then the assignment of "ord-1" group 1 is "UNSOURCEABLE"
    And the last command published "FulfillmentQueued, FulfillmentUnsourceable"

  Scenario: The release keeps the day and tells the vendor STANDARD
    Given shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1" and a window starting in 30 hours
    When I release the assignment of "ord-1" group 1
    Then the last release event carries tier STANDARD
    And the assignment of "ord-1" group 1 is "RELEASED"

  Scenario: A revised window start moves the group to the new day
    Given shopper "s1" places order "ord-1" with a CONSOLIDATED group of 2 units of "cat-1" and a window starting in 10 hours
    When promise "prm-ord-1-1" is revised to revision 1 with the window from 30 to 60 hours
    Then the assignment of "ord-1" group 1 targets the delivery wave "w2"
