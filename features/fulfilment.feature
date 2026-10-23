Feature: Shipping, delivery and cancellation
  A locked group ships with its tracking numbers and is delivered when its last package is.
  A cancel request is decided here: cancelled before the lock, refused after it (gap G12).

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"

  Scenario: A locked group ships and is delivered
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the vendor ships the packages "TRK1, TRK2" of "ord-1" group 1
    Then the last command published "FulfillmentShipped"
    And the assignment of "ord-1" group 1 is "SHIPPED"

  Scenario: The group is delivered when its last package is
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    And the vendor ships the packages "TRK1, TRK2" of "ord-1" group 1
    When package "TRK1" of "ord-1" group 1 is delivered
    Then the last command published nothing
    And the assignment of "ord-1" group 1 is "SHIPPED"
    When package "TRK2" of "ord-1" group 1 is delivered
    Then the last command published "FulfillmentDelivered"
    And the assignment of "ord-1" group 1 is "DELIVERED"

  Scenario: A package that is not on the shipment is refused
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    And the vendor ships the packages "TRK1" of "ord-1" group 1
    When package "NOPE" of "ord-1" group 1 is delivered
    Then the command is rejected as invalid

  Scenario: A queued group is cancelled on request
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelled"
    And the assignment of "ord-1" group 1 is "CANCELLED"

  Scenario: A released group is cancelled on request
    Given I release the assignment of "ord-1" group 1
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelled"
    And the assignment of "ord-1" group 1 is "CANCELLED"

  Scenario: A locked group refuses the cancel request and stays locked
    Given I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelRefused"
    And the assignment of "ord-1" group 1 is "LOCKED"
