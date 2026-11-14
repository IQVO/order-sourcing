Feature: The candidate search, the release point and the re-optimisation
  Rules of warehouse-docs network/docs/contexts/order-sourcing (use cases ReoptimiseAssignment and
  ReleaseToNode, aggregate canvas policies VirtualQueue and AssignmentCost, read models): which nodes
  and paths are candidates, when a group is released, and what a re-optimisation may change. The read
  models are fed through the projector, so the arrival order of sibling facts is part of the rule.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  # --- VirtualQueue: when a group leaves the queue -------------------------------------------

  Scenario: The release point is the pickup cutoff minus the handling time minus the vendor ack budget
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 is due for release 150 minutes after the start

  Scenario Outline: The sweep releases a group exactly at its release point and not a minute earlier
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the clock is advanced by <minutes> minutes
    When the sweep runs
    Then the sweep released <released> assignments

    Examples:
      | minutes | released |
      | 149     | 0        |
      | 150     | 1        |

  Scenario: A SAME_DAY group skips the queue and is released the moment it is queued
    Given a micro-fulfilment node "fc-m" ships straight to the delivery station "ds-1"
    When order "ord-1" is placed with a SAME_DAY group of 1 units of "cat-1"
    Then the last command published "FulfillmentQueued, FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the assignment of "ord-1" group 1 has a PENDING release number 1 to "fc-m"

  Scenario: A SAME_DAY group that no micro node can serve is unsourceable at once
    When order "ord-1" is placed with a SAME_DAY group of 1 units of "cat-1"
    Then the last command published "FulfillmentQueued, FulfillmentUnsourceable"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"

  # --- Invariant: a candidate's eta never exceeds deliverByLatest ----------------------------

  Scenario Outline: The delivery wave must end by deliverByLatest, to the minute
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1" and a deadline in <minutes> minutes
    Then the assignment of "ord-1" group 1 is "<state>"

    Examples:
      | minutes | state        |
      | 1560    | QUEUED       |
      | 1559    | UNSOURCEABLE |

  # --- Candidate search: what makes a node eligible ------------------------------------------

  Scenario Outline: A node must ship every line in full, one unit short is not enough
    When order "ord-1" is placed with a NEXT_DAY group of <units> units of "cat-1"
    Then the assignment of "ord-1" group 1 is "<state>"

    Examples:
      | units | state        |
      | 10    | QUEUED       |
      | 11    | UNSOURCEABLE |

  Scenario: A suppressed listing takes the node out of the search and a restored one brings it back
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the listing of "cat-1" at "fc-a" is suppressed
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
    Given the listing of "cat-1" at "fc-a" is restored
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"

  Scenario: A suspended node leaves the search and the queued group moves on
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" is suspended at hour 1
    When I re-optimise the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReassigned"
    And the last "FulfillmentReassigned" event has "FromNodeID" "fc-a"
    And the last "FulfillmentReassigned" event has "ToNodeID" "fc-b"
    And the last "FulfillmentReassigned" event has "Reason" "REOPTIMISED"

  Scenario: A node status that arrives out of order does not undo a newer one
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" is suspended at hour 5
    And the node "fc-a" is activated at hour 3
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
    Given the node "fc-a" is activated at hour 6
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"

  Scenario: A retired node never comes back
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" is retired at hour 2
    And the node "fc-a" is activated at hour 9
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"

  Scenario Outline: A declared operating profile applies only when its revision is newer
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" declares operating profile revision <revision> with a handling time of 300 minutes
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "<node>"

    Examples:
      | revision | node |
      | 2        | fc-b |
      | 1        | fc-a |

  Scenario Outline: A pickup departure with no place left cannot be booked, one place is enough
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the pickup departure "pk-a" reports <places> places left
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "<node>"

    Examples:
      | places | node |
      | 0      | fc-b |
      | 1      | fc-a |

  Scenario: A cancelled pickup departure is never a candidate
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the pickup departure "pk-a" is cancelled
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"

  Scenario Outline: A delivery wave with no stop left leaves no way to the customer, one stop is enough
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the delivery wave "w1" reports <stops> stops left
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 is "<state>"

    Examples:
      | stops | state        |
      | 0     | UNSOURCEABLE |
      | 1     | QUEUED       |

  Scenario: A closed lane takes the pickups that use it out of the search
    Given the pickup lane of "fc-a" costs 0 cents
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the lane of the pickup of "fc-a" is closed
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"

  Scenario Outline: A pickup must leave the node time to handle the order, to the minute
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the clock is advanced by <minutes> minutes
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 is "<state>"

    Examples:
      | minutes | state        |
      | 180     | QUEUED       |
      | 181     | UNSOURCEABLE |

  # --- AssignmentCost: ties and penalties ------------------------------------------------------

  Scenario: Equal in cost, the witness node wins the tie
    Given the node "fc-b" is registered in region "r1"
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"

  Scenario Outline: Only a graded 30-day scorecard costs a node the tie
    Given the node "fc-b" is registered in region "r1"
    And the node "fc-a" has a "<grade>" scorecard over the "<window>" window
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "<node>"

    Examples:
      | grade       | window | node |
      | B           | 30d    | fc-b |
      | D           | 7d     | fc-a |
      | PROVISIONAL | 30d    | fc-a |

  Scenario Outline: An exhausted promise book penalises exactly the node and pickup it names
    Given the node "fc-b" is registered in region "r1"
    And the promise book of the pickup departure "<departure>" of node "fc-a" is exhausted
    When order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    Then the assignment of "ord-1" group 1 has the candidate node "<node>"

    Examples:
      | departure | node |
      | pk-a      | fc-b |
      | pk-z      | fc-a |

  # --- ReoptimiseAssignment -----------------------------------------------------------------------

  Scenario: A re-optimisation that moves the best node moves the release point with it
    Given the pickup of "fc-b" has its cutoff at hour 6
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" has no stock of "cat-1" any more
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
    And the assignment of "ord-1" group 1 is due for release 270 minutes after the start

  Scenario: A re-optimisation on unchanged read models changes nothing, not even the version
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When I re-optimise the assignment of "ord-1" group 1
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 has version 1

  Scenario: A cheaper path to the same node updates the candidate silently
    Given the pickup lane of "fc-a" costs 1100 cents
    And the pickup lane of "fc-b" costs 1500 cents
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the assignment of "ord-1" group 1 has the estimated cost 1100 cents
    When the pickup lane of "fc-a" costs 900 cents
    And I re-optimise the assignment of "ord-1" group 1
    Then the last command published nothing
    And the assignment of "ord-1" group 1 has the candidate node "fc-a"
    And the assignment of "ord-1" group 1 has the estimated cost 900 cents
    And the assignment of "ord-1" group 1 has version 2

  Scenario: A re-optimisation that finds no candidate makes the group unsourceable
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" has no stock of "cat-1" any more
    And the node "fc-b" has no stock of "cat-1" any more
    When I re-optimise the assignment of "ord-1" group 1
    Then the last command published "FulfillmentUnsourceable"
    And the last "FulfillmentUnsourceable" event has "Reason" "NO_CANDIDATE"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"

  Scenario: A node that refused the group stays excluded when it becomes the best again
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node rejects release 1
    When I re-optimise the assignment of "ord-1" group 1
    Then the last command published nothing
    And the assignment of "ord-1" group 1 has the candidate node "fc-b"
    And the assignment of "ord-1" group 1 excludes the node "fc-a"

  Scenario: An unsourceable group cannot be re-optimised
    Given order "ord-1" is placed with a NEXT_DAY group of 1 units of "cat-unknown"
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 409
    And the response problem type is "illegal-transition"

  Scenario: A cancelled group cannot be re-optimised
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the customer cancels group 1 of order "ord-1"
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 409
    And the response problem type is "illegal-transition"

  Scenario: A shipped group is as locked as a locked one
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been shipped
    When a reoptimise request is posted for the assignment of "ord-1" group 1
    Then the response status is 409
    And the response problem type is "assignment-locked"

  # --- ReleaseToNode ------------------------------------------------------------------------------

  Scenario: A release re-checks the stock and goes to the next node when the candidate lost it
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" has no stock of "cat-1" any more
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentReassigned, FulfillmentReleasedToNode, FulfillmentSourcingDecided"
    And the last "FulfillmentReassigned" event has "Reason" "RELEASE_RECHECK"
    And the assignment of "ord-1" group 1 has a PENDING release number 1 to "fc-b"

  Scenario: A release with no candidate left makes the group unsourceable and sends nothing
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" has no stock of "cat-1" any more
    And the node "fc-b" has no stock of "cat-1" any more
    When I release the assignment of "ord-1" group 1
    Then the last command published "FulfillmentUnsourceable"
    And the last "FulfillmentUnsourceable" event has "Reason" "NO_CANDIDATE"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"
    And the assignment of "ord-1" group 1 has 0 releases

  Scenario: A release names the node's listing and the pickup cutoff as the required ship-by
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When I release the assignment of "ord-1" group 1
    Then the last release event goes to node "fc-a" as release number 1 with the listing "lst-1-fc-a" and required ship-by at hour 4

  Scenario: A second firing of the release timer finds the group released and stops
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When I release the assignment of "ord-1" group 1
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 has 1 release

  # --- Read models: any order, any redelivery -----------------------------------------------------

  Scenario: A redelivered sibling event changes nothing
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the pickup departure "pk-a" reports 0 places left in event "evt-cap-9"
    And the pickup departure "pk-a" reports 5 places left in event "evt-cap-9"
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"

  Scenario: A late availability fact does not overwrite a newer one
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-a" reports 0 units of "cat-1" as of hour 5
    And the node "fc-a" reports 10 units of "cat-1" as of hour 2
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-b"
    Given the node "fc-a" reports 10 units of "cat-1" as of hour 7
    When I re-optimise the assignment of "ord-1" group 1
    Then the assignment of "ord-1" group 1 has the candidate node "fc-a"
