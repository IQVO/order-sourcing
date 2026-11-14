Feature: Queueing, refusal, lock, shipment, delivery, cancellation and revision rules
  Rules of warehouse-docs network/docs/contexts/order-sourcing (use cases QueueAssignment,
  HandleNodeRefusal, LockAssignment, MarkShipped, MarkDelivered, CancelAssignment and
  ApplyRevisedPromise; the aggregate's invariants and state transitions) that no earlier scenario
  pins: idempotent replays, late and unknown outcomes, illegal-transition rows, per-group cancel
  answers and what a revised promise does in each state.

  Background:
    Given a network with vendor nodes "fc-a" and "fc-b" that both reach the delivery station "ds-1"

  # --- QueueAssignment ----------------------------------------------------------------------------

  Scenario: One placed order queues one assignment per shipment group
    When order "ord-5" is placed with these groups:
      | groupNo | tier     | units | item  |
      | 1       | NEXT_DAY | 2     | cat-1 |
      | 2       | STANDARD | 1     | cat-1 |
    Then the last command published "FulfillmentQueued, FulfillmentQueued"
    And there are exactly 2 assignments
    And the assignment of "ord-5" group 1 is "QUEUED"
    And the assignment of "ord-5" group 2 is "QUEUED"

  Scenario: A repeated order under a new event id is stopped by the unique (order, group) key
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When order "ord-1" is placed again under a new event id
    Then the command succeeds
    And the last command published nothing
    And there is exactly 1 assignment

  Scenario: A replay of an order queues only the groups it lacks
    Given order "ord-5" is placed with these groups:
      | groupNo | tier     | units | item  |
      | 1       | NEXT_DAY | 2     | cat-1 |
    When order "ord-5" is placed with these groups:
      | groupNo | tier     | units | item  |
      | 1       | NEXT_DAY | 2     | cat-1 |
      | 2       | STANDARD | 1     | cat-1 |
    Then the last command published "FulfillmentQueued"
    And there are exactly 2 assignments

  # --- HandleNodeRefusal --------------------------------------------------------------------------

  Scenario: A rejection records the vendor's reason code on the release and on the exclusion
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node rejects release 1 with the reason code "OUT_OF_STOCK"
    Then the assignment of "ord-1" group 1 has release number 1 with the outcome "REJECTED"
    And the assignment of "ord-1" group 1 has release number 1 with the reason code "OUT_OF_STOCK"
    And the assignment of "ord-1" group 1 excluded the node "fc-a" as "REJECTED:OUT_OF_STOCK"
    And the last "FulfillmentReassigned" event has "Reason" "PURCHASE_ORDER_REJECTED"
    And the last "FulfillmentReassigned" event has "FromNodeID" "fc-a"
    And the last "FulfillmentReassigned" event has "ToNodeID" "fc-b"

  Scenario: An expiry marks the release EXPIRED and excludes the node as EXPIRED
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node lets release 1 expire
    Then the assignment of "ord-1" group 1 has release number 1 with the outcome "EXPIRED"
    And the assignment of "ord-1" group 1 excluded the node "fc-a" as "EXPIRED"
    And the last "FulfillmentReassigned" event has "Reason" "PURCHASE_ORDER_EXPIRED"

  Scenario: A refusal that arrives after the lock is refused and the node stays
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the node rejects release 1
    Then the command is refused with "ErrAssignmentLocked"
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-1" group 1 has release number 1 with the outcome "ACCEPTED"

  Scenario: The late outcome of an old release does not disturb the release now pending
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node rejects release 1
    And I release the assignment of "ord-1" group 1
    When the node lets release 1 of "ord-1" group 1 expire
    Then the command is refused with "ErrReleaseNotPending"
    And the last command published nothing
    And the assignment of "ord-1" group 1 has a PENDING release number 2 to "fc-b"
    And the assignment of "ord-1" group 1 has release number 1 with the outcome "REJECTED"

  Scenario: An outcome for a release that was never issued is not pending
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When the node rejects release 7
    Then the command is refused with "ErrReleaseNotPending"
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "QUEUED"

  Scenario: A refusal whose release point has already passed is released again at once
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the clock is advanced by 180 minutes
    And I release the assignment of "ord-1" group 1
    And the node rejects release 1
    When the sweep runs
    Then the sweep released 1 assignment
    And the assignment of "ord-1" group 1 has a PENDING release number 2 to "fc-b"

  Scenario: A refusal with no next candidate makes the group unsourceable and lists the refusing node
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the node "fc-b" has no stock of "cat-1" any more
    And I release the assignment of "ord-1" group 1
    When the node rejects release 1
    Then the last command published "FulfillmentUnsourceable"
    And the last "FulfillmentUnsourceable" event has "Reason" "NODE_REFUSED"
    And the last "FulfillmentUnsourceable" event lists 1 "ExcludedNodes"
    And the assignment of "ord-1" group 1 is "UNSOURCEABLE"

  # --- LockAssignment -----------------------------------------------------------------------------

  Scenario: A repeated acceptance of the locking release is a no-op
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the node accepts release 1
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 has version 3

  Scenario: An acceptance of another release on a locked group is refused
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node accepts release 1
    When the node accepts release 2
    Then the command is refused with "ErrAssignmentLocked"
    And the assignment of "ord-1" group 1 is "LOCKED"

  Scenario: An acceptance for a group that was never released locks nothing
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When the node accepts release 1
    Then the command is refused with "ErrReleaseNotPending"
    And the assignment of "ord-1" group 1 is "QUEUED"

  Scenario: An acceptance for a cancelled group changes nothing
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the customer cancels group 1 of order "ord-1"
    When the node accepts release 1
    Then the last command published nothing
    And the assignment of "ord-1" group 1 is "CANCELLED"

  Scenario: The lock records the acceptance instant, the reason, the node and the booked pickup
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the node accepts release 1
    Then the assignment of "ord-1" group 1 was locked at hour 1 because "PURCHASE_ORDER_ACCEPTED" on the pickup departure "pk-a"
    And the last "FulfillmentLocked" event has "NodeID" "fc-a"
    And the assignment of "ord-1" group 1 has release number 1 with the outcome "ACCEPTED"

  # --- MarkShipped and MarkDelivered --------------------------------------------------------------

  Scenario: A shipment is refused unless the group is locked
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the vendor ships the packages "TRK1" of "ord-1" group 1
    Then the command is refused as an illegal transition
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "RELEASED"

  Scenario: A repeated shipment keeps the first tracking numbers
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been locked
    And the vendor ships the packages "TRK1" of "ord-1" group 1
    When the vendor ships the packages "TRK9" of "ord-1" group 1
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 shipped the packages "TRK1" at hour 2

  Scenario: A delivery before the shipment is refused
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been locked
    When package "TRK1" of "ord-1" group 1 is delivered
    Then the command is refused as an illegal transition
    And the assignment of "ord-1" group 1 is "LOCKED"

  Scenario: A package reported twice is counted once and the last package delivers the group
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been locked
    And the vendor ships the packages "TRK1, TRK2" of "ord-1" group 1
    And package "TRK1" of "ord-1" group 1 is delivered
    When package "TRK1" of "ord-1" group 1 is delivered
    Then the last command published nothing
    And the assignment of "ord-1" group 1 is "SHIPPED"
    When package "TRK2" of "ord-1" group 1 is delivered
    Then the last command published "FulfillmentDelivered"
    And the assignment of "ord-1" group 1 is "DELIVERED"
    And the assignment of "ord-1" group 1 was delivered at hour 30

  Scenario: A package reported after the group was delivered is a no-op
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been delivered
    When package "TRK1" of "ord-1" group 1 is delivered
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "DELIVERED"

  # --- CancelAssignment ---------------------------------------------------------------------------

  Scenario: One cancel request answers every group it lists
    Given order "ord-5" is placed with these groups:
      | groupNo | tier     | units | item  |
      | 1       | NEXT_DAY | 2     | cat-1 |
      | 2       | STANDARD | 1     | cat-1 |
    When the customer cancels groups "1, 2" of order "ord-5"
    Then the last command published "FulfillmentCancelled, FulfillmentCancelled"
    And the assignment of "ord-5" group 1 is "CANCELLED"
    And the assignment of "ord-5" group 2 is "CANCELLED"

  Scenario: Each group of a cancel request is answered on its own state
    Given order "ord-5" is placed with these groups:
      | groupNo | tier     | units | item  |
      | 1       | NEXT_DAY | 2     | cat-1 |
      | 2       | STANDARD | 1     | cat-1 |
    And the group of order "ord-5" has been locked
    When the customer cancels groups "1, 2" of order "ord-5"
    Then the last command published "FulfillmentCancelRefused, FulfillmentCancelled"
    And the assignment of "ord-5" group 1 is "LOCKED"
    And the assignment of "ord-5" group 2 is "CANCELLED"

  Scenario: A group this service never queued is skipped without failing the request
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When the customer cancels groups "1, 9" of order "ord-1"
    Then the command succeeds
    And the last command published "FulfillmentCancelled"

  Scenario: A repeated cancel request is a no-op
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the customer cancels group 1 of order "ord-1"
    When the customer cancels group 1 of order "ord-1"
    Then the command succeeds
    And the last command published nothing
    And the assignment of "ord-1" group 1 is "CANCELLED"

  Scenario Outline: A cancel request after shipment or delivery is refused like one after the lock
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been <progress>
    When the customer cancels group 1 of order "ord-1"
    Then the last command published "FulfillmentCancelRefused"
    And the last "FulfillmentCancelRefused" event has "Reason" "CANCEL_AFTER_LOCK"
    And the assignment of "ord-1" group 1 is "<state>"

    Examples:
      | progress  | state     |
      | shipped   | SHIPPED   |
      | delivered | DELIVERED |

  Scenario: A refused cancel keeps the state but moves the version
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the group of order "ord-1" has been locked
    And the assignment of "ord-1" group 1 has version 3
    When the customer cancels group 1 of order "ord-1"
    Then the assignment of "ord-1" group 1 is "LOCKED"
    And the assignment of "ord-1" group 1 has version 4

  Scenario Outline: The cancel reason comes from the event and defaults to UNSPECIFIED
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When the customer cancels group 1 of order "ord-1" with the reason "<sent>"
    Then the last "FulfillmentCancelled" event has "Reason" "<recorded>"
    And the assignment of "ord-1" group 1 was cancelled because "<recorded>"

    Examples:
      | sent         | recorded     |
      | CHANGED_MIND | CHANGED_MIND |
      |              | UNSPECIFIED  |

  Scenario: Cancelling a released group leaves its release PENDING for the vendor side to cancel
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When the customer cancels group 1 of order "ord-1"
    Then the assignment of "ord-1" group 1 is "CANCELLED"
    And the assignment of "ord-1" group 1 has a PENDING release number 1 to "fc-a"

  # --- ApplyRevisedPromise ------------------------------------------------------------------------

  Scenario: A revised promise re-queues an unsourceable group
    Given order "ord-9" is placed with a NEXT_DAY group of 2 units of "cat-1" and a deadline in 1200 minutes
    And the assignment of "ord-9" group 1 is "UNSOURCEABLE"
    When promise "prm-ord-9-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-9" group 1 is "QUEUED"
    And the assignment of "ord-9" group 1 has the candidate node "fc-a"

  Scenario: A revised promise that still leaves nothing keeps the group unsourceable without a second event
    Given order "ord-9" is placed with a NEXT_DAY group of 2 units of "cat-1" and a deadline in 1200 minutes
    When promise "prm-ord-9-1" is revised to revision 1 with the deadline in 24 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-9" group 1 is "UNSOURCEABLE"
    And the assignment of "ord-9" group 1 has promise revision 1

  Scenario: A revision resets the exclusions, so the refusing node can win the group back
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    And the node rejects release 1
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published "FulfillmentConstraintRevised, FulfillmentReassigned"
    And the last "FulfillmentReassigned" event has "Reason" "PROMISE_REVISED"
    And the last "FulfillmentReassigned" event has "FromNodeID" "fc-b"
    And the last "FulfillmentReassigned" event has "ToNodeID" "fc-a"
    And the assignment of "ord-1" group 1 has the candidate node "fc-a"

  Scenario: The witness node of a revision wins a tie
    Given the node "fc-b" is registered in region "r1"
    And order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours and the witness node "fc-b"
    Then the assignment of "ord-1" group 1 has the witness node "fc-b"
    And the assignment of "ord-1" group 1 has the candidate node "fc-b"
    And the last command published "FulfillmentConstraintRevised, FulfillmentReassigned"

  Scenario: A revision of a released group changes only the constraint and leaves the pending release
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And I release the assignment of "ord-1" group 1
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published "FulfillmentConstraintRevised"
    And the assignment of "ord-1" group 1 is "RELEASED"
    And the assignment of "ord-1" group 1 has promise revision 1
    And the assignment of "ord-1" group 1 has a PENDING release number 1 to "fc-a"

  Scenario: A revision is ignored by a cancelled group
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And the customer cancels group 1 of order "ord-1"
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    Then the last command published nothing
    And the assignment of "ord-1" group 1 is "CANCELLED"
    And the assignment of "ord-1" group 1 has promise revision 0

  Scenario: A revision number equal to the applied one is ignored
    Given order "ord-1" is placed with a NEXT_DAY group of 2 units of "cat-1"
    And promise "prm-ord-1-1" is revised to revision 1 with the deadline in 60 hours
    When promise "prm-ord-1-1" is revised to revision 1 with the deadline in 70 hours
    Then the last command published nothing
    And the assignment of "ord-1" group 1 has promise revision 1
