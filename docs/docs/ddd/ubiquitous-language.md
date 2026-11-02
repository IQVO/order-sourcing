---
id: ubiquitous-language
title: Ubiquitous language
sidebar_label: Ubiquitous language
description: "The terms Order Sourcing speaks (assignment, virtual queue, release, path lock, reassignment, unsourceable) and the neighbour terms it borrows, each with its code identifier."
---

# Ubiquitous language

The terms of this bounded context, with the identifier the code uses (`package.Type`, constant or field) and what each term is
not. The network-wide glossary is in the aggregator's
[Ubiquitous Language](https://iqvo.github.io/warehouse-docs/network/strategic-design/ubiquitous-language). Identifiers are read
from `internal/domain/assignment`, `internal/domain/sourcing`, `internal/domain/network` and `internal/application/usecases`.

## This context's terms

| Term | Definition | Code identifier | Not to be confused with |
| --- | --- | --- | --- |
| **Fulfillment assignment** | The decision of which node and path fulfil one shipment group. One per `(orderId, groupNo)`. | `assignment.Assignment` (table `assignments`) | The **promise**, which only proves one feasible path exists. |
| **Virtual queue** | Where `QUEUED` assignments wait, re-optimisable, until their release point. It is not a store of its own: the rows in state `QUEUED`, ordered by `releaseAt`. | `assignment.StateQueued`, `assignments_queue_idx`, `usecases.SweepQueue` | A queue at the vendor node. Nothing has reached any node while an assignment is queued. |
| **Release (to node)** | Sending a group to one node; for a vendor node this issues a purchase order. | `Assignment.Release`, `assignment.Release`, use case `ReleaseToNode`, event `FulfillmentReleasedToNode` | A promise release (a provisional hold that expired in `delivery-promise`). |
| **Path lock** | The point after which an assignment never changes node: the vendor's acceptance. | `Assignment.Lock`, `assignment.StateLocked`, `ErrAssignmentLocked`, event `FulfillmentLocked` | The vendor's pick, which the network cannot see. The acceptance stands in for it. |
| **Reassignment** | Moving a group to another node after a refusal, or while still queued. | `Assignment.RefuseRelease`, `Reoptimise`, event `FulfillmentReassigned` | A **re-promise**, which changes the customer's window. A reassignment never does. |
| **Unsourceable** | No node can still meet the promise; the group goes back to `delivery-promise`. | `assignment.StateUnsourceable`, event `FulfillmentUnsourceable` | A broken promise. Unsourceable is raised before the deadline, so the promise can still be revised. |
| **Promise constraint** | What the assignment must honour: `promiseId`, `tier`, `deliverByLatest`, `witnessNodeId`, `revisionNo`. | `assignment.PromiseConstraint` | The full promise terms of `delivery-promise`. Only these fields cross. |
| **Candidate** | One feasible way to fulfil the group: node, pickup departure, legs, delivery wave, `eta`, `cost`. | `assignment.Candidate` | The **witness path**. The witness is one candidate, the best may be another. |
| **Release record** | One attempt to send the group to a node: `releaseNo`, `nodeId`, `releasedAt`, `requiredShipBy`, `outcome` (`PENDING`, `ACCEPTED`, `REJECTED`, `EXPIRED`), `reasonCode`. | `assignment.Release`, `assignment.Outcome` | The purchase order. A release names its purchase order by `assignmentId` + `releaseNo`; `vendor-connectivity` owns the order. |
| **Release point** | The instant an assignment leaves the queue: best candidate's pickup cutoff minus node handling time minus the 30 minute ack budget, never before now; now for `SAME_DAY`. | `releaseAt`, `sourcing.AckBudget` | `ackBy`, the vendor's acknowledgement deadline, computed by `vendor-connectivity` after the release. |
| **Vendor ack budget** | The time reserved, before handling must start, for the vendor to acknowledge. | `sourcing.AckBudget` (30 minutes) | The vendor's own ack window. |
| **Excluded node** | A node that refused this group, with the reason (`REJECTED:<reasonCode>` or `EXPIRED`). Never chosen again for the same promise revision. | `assignment.ExcludedNode`, `ErrNodeExcluded` | A suspended node, which is unavailable to every group. |
| **Assignment cost** | `100` per onward leg, `10` per hour of wait, `1000` cross-region, a scorecard penalty, `500` book pressure; ties go to the witness node. | `sourcing.LegCost`, `HourCost`, `CrossRegionPenalty`, `BookPressurePenalty`, `Candidate.Cost` | The scorecard handicap of `delivery-promise`, which delays a promise. |
| **Book pressure** | Knowing that a node's promise book for a cutoff is exhausted, used as a cost penalty. | `network.Pressure`, table `rm_book_pressure` | Departure capacity, which `linehaul-transportation` owns. |
| **Assignment version** | A counter that strictly increases with every change of the assignment. | `Assignment.Version`, column `version`, `ErrStaleAssignmentVersion` | `node-topology`'s service-area `assignmentVersion`, an unrelated counter with the same name. |
| **Revision number** | The applied revision of the promise. A higher one resets the exclusion list. | `PromiseConstraint.RevisionNo`, `ErrStaleRevision` | The assignment version. |
| **Fill-or-kill** | A group goes to one node in full; a partial answer is not a thing. | `Assignment.Release` with `checkReleaseLines`, `sourcing.ResolveLines` | Shipment grouping, decided earlier by `delivery-promise`. |
| **Cancel refused** | A cancel request that came after the lock; the state stays and the version moves. | `Assignment.RefuseCancel`, `ErrCancelAfterLock`, event `FulfillmentCancelRefused` | A cancellation. |
| **World** | The immutable snapshot of the read models one search reads. | `sourcing.World`, port `WorldReader` | The aggregate. |
| **Inbox, outbox** | The rows that make consumers idempotent and publishing transactional. | `processed_events`, `outbox_events`, ports `Inbox`, `EventOutbox` | Kafka offsets. |

## Neighbour terms this context uses

| Term | Definition (from the owner) | Owner | Not to be confused with |
| --- | --- | --- | --- |
| **Shipment group** | The lines of an order promised and shipped together from one node; here `groupNo`. | `delivery-promise` | A package. A group may ship as several packages (`trackingNumbers`). |
| **Promise** | The network's commitment of a delivery window for one shipment group. | `delivery-promise` | The assignment, which decides how the promise is kept. |
| **Witness path** | The concrete node, pickup, departures and delivery wave that prove a promise feasible; here `witnessNodeId`. | `delivery-promise` | The assigned path. |
| **Service tier** | `SAME_DAY`, `NEXT_DAY`, `TWO_DAY` or `STANDARD`. | `delivery-promise` | none |
| **Re-promise** | Revising a confirmed promise to a later window; event `PromiseRevised`. | `delivery-promise` | A reassignment. |
| **Promise book** | Capacity ledger of one node pickup cutoff on the promise side; event `PromiseBookExhausted`. | `delivery-promise` | Departure capacity. |
| **Purchase order** | The instruction to one vendor node to ship one shipment group in full by `requiredShipBy`. | `vendor-connectivity` | The release. One release produces at most one purchase order. |
| **Acknowledgement** | The vendor's single, whole-order accept or reject. | `vendor-connectivity` | The lock. Only an accept locks. |
| **Required ship-by** | The pickup cutoff the purchase order must be ready for; here `Release.requiredShipBy`. | `vendor-connectivity` | The promise's ship-by at the witness node. |
| **Vendor** | An organisation operating fulfilment nodes for the network. | `vendor-connectivity` | A node. |
| **Node** | One physical building of the network, of exactly one node type. | `node-topology` | none |
| **Node operator** | Who runs the building: network or a vendor. Every release target is `VENDOR`-operated. | `node-topology` | none |
| **Operational profile** | The node's declared handling time, cutoffs and hours, versioned by `profileRevision`. | `node-topology` | The measured scorecard. |
| **Region** | A largely self-sufficient partition of the network; drives the cross-region penalty. | `node-topology` | none |
| **Service area** | The postal prefixes one delivery station serves; matched exactly to `destinationPostalPrefix`. | `node-topology` | none |
| **Lane** | A directed, mode-specific connection between two nodes. | `node-topology` | A departure on that lane. |
| **Listing** | A catalogue item offered from one node; a release carries `catalogItemId` and `listingId`. | `offer-availability` | The catalogue item. |
| **Available quantity** | What a node claims it can ship now for one listing. | `offer-availability` | Stock reserved for this group. This context reserves nothing. |
| **Pickup departure** | The departure that collects packages at a vendor node at one of its cutoffs. | `linehaul-transportation` | none |
| **Departure** | One scheduled trip on a lane with a cutoff and a capacity. | `linehaul-transportation` | none |
| **Delivery wave** | A station's dispatch slot: induct cutoff, delivery window and stop capacity. | `last-mile-delivery` | none |
| **Scorecard, grade** | A node's measured performance over a window, graded A to D or PROVISIONAL. | `node-performance` | The declared profile. |
| **Tracking number** | The network-wide unique id of one package. | `shipping-and-tracking` | The group. |
| **Customer order** | What the shopper bought, with a promise per shipment group. | `customer-ordering` | The assignment, one per group of the order. |

No fleet translation table is needed here: this context never touches the warehouse-systems fleet. Only `vendor-connectivity`
does, through its Vendor API.
