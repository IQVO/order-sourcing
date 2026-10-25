import type { Assignment, AssignmentList, Candidate, Release } from "../types";

/** The QUEUED fixture: the OpenAPI-style shape of a fresh assignment. */
export function queuedAssignment(over: Partial<Assignment> = {}): Assignment {
  return {
    assignmentId: "fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f",
    orderId: "ord-2026-0001",
    groupNo: 1,
    destinationPostalPrefix: "30301",
    state: "QUEUED",
    assignmentVersion: 1,
    promise: {
      promiseId: "prm-9f2a",
      tier: "TWO_DAY",
      deliverByLatest: "2026-10-26T21:00:00Z",
      witnessNodeId: "node-214",
      revisionNo: 3,
    },
    lines: [{ lineNo: 1, catalogItemId: "cat-0442", quantity: 2 }],
    candidate: candidate(),
    releaseAt: "2026-10-24T14:00:00Z",
    releases: [],
    excludedNodes: [{ nodeId: "node-088", reason: "no-capacity" }],
    trackingNumbers: [],
    ...over,
  };
}

export function candidate(over: Partial<Candidate> = {}): Candidate {
  return {
    nodeId: "node-214",
    pickupDepartureId: "dep-5512",
    pickupCutoffAt: "2026-10-24T17:00:00Z",
    legs: ["node-214>hub-3", "hub-3>dst-30301"],
    deliveryWaveId: "wave-118",
    eta: "2026-10-25T09:30:00Z",
    cost: 130,
    ...over,
  };
}

export function release(over: Partial<Release> = {}): Release {
  return {
    releaseNo: 1,
    nodeId: "node-214",
    releasedAt: "2026-10-24T14:00:00Z",
    requiredShipBy: "2026-10-24T17:00:00Z",
    outcome: "PENDING",
    ...over,
  };
}

/** The RELEASED fixture: version 2, one pending release to node-214. */
export function releasedAssignment(over: Partial<Assignment> = {}): Assignment {
  return queuedAssignment({
    state: "RELEASED",
    assignmentVersion: 2,
    candidate: undefined,
    lines: [{ lineNo: 1, catalogItemId: "cat-0442", listingId: "lst-77", quantity: 2 }],
    releases: [release()],
    ...over,
  });
}

/** One page of two assignments, ordered by assignmentId ascending. */
export function assignmentPage(over: Partial<AssignmentList> = {}): AssignmentList {
  return {
    assignments: [
      queuedAssignment(),
      releasedAssignment({
        assignmentId: "fa-2c5f39ab-40b2-5e4d-9a7f-1b2c3d4e5f60",
        orderId: "ord-2026-0002",
        groupNo: 2,
        destinationPostalPrefix: "10001",
      }),
    ],
    ...over,
  };
}
