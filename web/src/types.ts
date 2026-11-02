/** Wire types of apis/openapi.yaml (camelCase, as the REST contract names them). */

/** The lifecycle states of a FulfillmentAssignment. */
export const STATES = [
  "QUEUED",
  "RELEASED",
  "LOCKED",
  "SHIPPED",
  "DELIVERED",
  "UNSOURCEABLE",
  "CANCELLED",
] as const;
export type AssignmentState = (typeof STATES)[number];

/** The promise tiers a constraint can carry. */
export const TIERS = ["SAME_DAY", "NEXT_DAY", "TWO_DAY", "STANDARD"] as const;
export type Tier = (typeof TIERS)[number];

/** The outcome of one release to a node. */
export const OUTCOMES = ["PENDING", "ACCEPTED", "REJECTED", "EXPIRED"] as const;
export type Outcome = (typeof OUTCOMES)[number];

/** The promise constraint the assignment must keep (the OpenAPI Promise schema). */
export interface PromiseConstraint {
  promiseId: string;
  tier: Tier;
  deliverByLatest: string;
  witnessNodeId: string;
  revisionNo: number;
}

/** One line of the shipment group (the OpenAPI Line schema). */
export interface Line {
  lineNo: number;
  catalogItemId: string;
  listingId?: string;
  quantity: number;
}

/** The current best node and path while QUEUED (the OpenAPI Candidate schema). */
export interface Candidate {
  nodeId: string;
  pickupDepartureId: string;
  pickupCutoffAt: string;
  legs: string[];
  deliveryWaveId: string;
  eta: string;
  /** The path's cost in cost points. */
  cost: number;
}

/** One release to a node in the VirtualQueue's hand-off history. */
export interface Release {
  releaseNo: number;
  nodeId: string;
  releasedAt: string;
  requiredShipBy: string;
  outcome: Outcome;
  reasonCode?: string;
}

/** A node that refused the group (or was skipped) and why. */
export interface ExcludedNode {
  nodeId: string;
  reason: string;
}

/** One FulfillmentAssignment: the answer of GET /assignments/{assignmentId}. */
export interface Assignment {
  assignmentId: string;
  orderId: string;
  groupNo: number;
  destinationPostalPrefix: string;
  state: AssignmentState;
  assignmentVersion: number;
  promise: PromiseConstraint;
  lines: Line[];
  candidate?: Candidate;
  releaseAt?: string;
  releases: Release[];
  excludedNodes: ExcludedNode[];
  lockedAt?: string;
  lockReason?: string;
  pickupDepartureId?: string;
  trackingNumbers: string[];
  shippedAt?: string;
  deliveredAt?: string;
  cancelReason?: string;
}

/** The wire answer of GET /assignments: one keyset page, ordered by assignmentId ascending. */
export interface AssignmentPage {
  items: Assignment[];
  /** Opaque; absent on the last page. */
  nextCursor?: string;
}

/** All pages of GET /assignments, followed until nextCursor is absent. */
export interface AssignmentList {
  assignments: Assignment[];
}

/** The query of GET /assignments: every filter is optional. */
export interface ListAssignmentsQuery {
  state?: AssignmentState;
  nodeId?: string;
  orderId?: string;
  limit?: number;
}
