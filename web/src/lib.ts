import { NO_VALUE } from "@warehouse/ui-kit";
import { STATES } from "./types";
import type { AssignmentState } from "./types";

/** "2026-10-24 14:03:00Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 19)}Z`;
}

/** A path's cost in cost points: 130 -> "130 pts". */
export function costLabel(cost: number): string {
  return `${cost} pts`;
}

/** "fa-1b4e…4e5f" -- the two ends of a long id, for table columns. */
export function idLabel(id: string): string {
  return id.length <= 12 ? id : `${id.slice(0, 9)}…${id.slice(-4)}`;
}

const isState = (value: string): value is AssignmentState =>
  (STATES as readonly string[]).includes(value);

/** The state a URL/typed value carries, or null when it is not one of the
 *  seven lifecycle states (the contract's validation-failed set). */
export function parseState(raw: string): AssignmentState | null {
  return isState(raw) ? raw : null;
}

/** The assignment id rule of the contract (the OpenAPI AssignmentId
 *  parameter): the service issues "fa-<uuid>" ids; free text is allowed in a
 *  lookup as long as it is non-empty, carries no whitespace or control
 *  characters, and no "/". */
export function assignmentIdProblem(id: string): string | null {
  if (id.length === 0) return "Enter an assignment id.";
  if (id.length > 64) return "An assignment id is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return "An assignment id cannot contain spaces, control characters or '/'.";
  return null;
}

/** The node id rule of the contract (the OpenAPI nodeId query): free text,
 *  non-empty, no whitespace or control characters, no "/". */
export function nodeIdProblem(id: string): string | null {
  if (id.length === 0) return "Enter a node id.";
  if (id.length > 64) return "A node id is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return "A node id cannot contain spaces, control characters or '/'.";
  return null;
}

/** The order id rule of the contract (the OpenAPI orderId query). */
export function orderIdProblem(id: string): string | null {
  if (id.length === 0) return "Enter an order id.";
  if (id.length > 64) return "An order id is at most 64 characters.";
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return "An order id cannot contain spaces, control characters or '/'.";
  return null;
}

/** The node an assignment currently sits on: the QUEUED candidate's node, or
 *  the node of the latest release from RELEASED on. "" when neither exists. */
export function currentNode(a: { candidate?: { nodeId: string }; releases: { nodeId: string }[] }): string {
  if (a.candidate) return a.candidate.nodeId;
  const releases = a.releases ?? [];
  return releases.length > 0 ? releases[releases.length - 1].nodeId : "";
}
