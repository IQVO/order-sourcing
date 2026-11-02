import { SOURCING_API_BASE } from "./config";
import type { Assignment, AssignmentList, AssignmentPage, ListAssignmentsQuery } from "./types";

/**
 * RFC 9457 problem+json body every error response from order-sourcing
 * returns (`type` is the bare slug: not-found, validation-failed).
 */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `not-found`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** The problem's type slug (e.g. "not-found"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

type Query = Record<string, string | number | boolean | undefined>;

export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * The one door to the service. Only this context's own API is ever called
 * (`SOURCING_API_BASE` = <apiOrigin>/api/order-sourcing): no sibling
 * context, no other service. This remote is read-only, so every request is
 * a header-less GET -- a simple CORS request. Re-optimising an assignment
 * (POST /reoptimisations) stays a REST operation of the API, not of this console.
 */
async function request<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${SOURCING_API_BASE}${path}`);
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return (await res.json()) as T;
}

const seg = encodeURIComponent;

/**
 * GET /assignments?state=&orderId=&nodeId=&limit=&cursor= -- ordered by assignmentId.
 * Follows nextCursor until it is absent (REST guideline 5.2: never assume one page);
 * `limit` is the page size, not a cap on the result.
 */
export async function listAssignments(q: ListAssignmentsQuery = {}): Promise<AssignmentList> {
  const assignments: Assignment[] = [];
  let cursor: string | undefined;
  do {
    const page = await request<AssignmentPage>(
      withQuery("/assignments", { state: q.state, orderId: q.orderId, nodeId: q.nodeId, limit: q.limit, cursor }),
    );
    assignments.push(...(page.items ?? []));
    cursor = page.nextCursor || undefined;
  } while (cursor);
  return { assignments };
}

/** GET /assignments/{assignmentId} (404 not-found: no assignment has that id). */
export async function getAssignment(assignmentId: string): Promise<Assignment> {
  return request<Assignment>(`/assignments/${seg(assignmentId)}`);
}
