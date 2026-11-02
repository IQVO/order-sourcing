import { afterEach, describe, expect, it } from "vitest";
import { getAssignment, listAssignments } from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { assignmentPage, queuedAssignment } from "./test/fixtures";

afterEach(() => {
  // fetch is unstubbed by src/test/setup.ts; nothing else to clean here.
});

describe("listAssignments", () => {
  it("sends the optional filters as query parameters", async () => {
    const api = mockApi({
      "GET /assignments": json(assignmentPage()),
    });
    await listAssignments({ state: "QUEUED", orderId: "ord-2026-0001", nodeId: "node-214", limit: 25 });
    const call = api.to("GET /assignments")[0];
    expect(call.path).toBe("/assignments");
    expect(call.url).toContain("?");
    expect(call.query.get("state")).toBe("QUEUED");
    expect(call.query.get("orderId")).toBe("ord-2026-0001");
    expect(call.query.get("nodeId")).toBe("node-214");
    expect(call.query.get("limit")).toBe("25");
  });

  it("omits the absent filters entirely", async () => {
    const api = mockApi({ "GET /assignments": json(assignmentPage()) });
    await listAssignments();
    const call = api.to("GET /assignments")[0];
    expect(call.url).not.toContain("?");
    expect(call.query.get("state")).toBeNull();
    expect(call.query.get("orderId")).toBeNull();
    expect(call.query.get("nodeId")).toBeNull();
    expect(call.query.get("limit")).toBeNull();
    expect(api.calls).toHaveLength(1);
  });

  it("normalizes an absent items array to an empty result", async () => {
    mockApi({ "GET /assignments": json({}) });
    const page = await listAssignments({ state: "CANCELLED" });
    expect(page.assignments).toEqual([]);
  });
});

describe("listAssignments paging", () => {
  it("follows nextCursor until it is absent", async () => {
    const [a, b] = assignmentPage().items;
    const api = mockApi({
      "GET /assignments": (req) =>
        req.query.get("cursor") === "c2" ? json({ items: [b] }) : json({ items: [a], nextCursor: "c2" }),
    });
    const got = await listAssignments({ state: "QUEUED", limit: 1 });
    expect(got.assignments.map((x) => x.assignmentId)).toEqual([a.assignmentId, b.assignmentId]);
    const calls = api.to("GET /assignments");
    expect(calls).toHaveLength(2);
    expect(calls[1].query.get("state")).toBe("QUEUED");
    expect(calls[1].query.get("cursor")).toBe("c2");
  });
});

describe("getAssignment", () => {
  it("reads one assignment with its promise and lines", async () => {
    mockApi({ "GET /assignments/fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f": json(queuedAssignment()) });
    const got = await getAssignment("fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f");
    expect(got.state).toBe("QUEUED");
    expect(got.promise.tier).toBe("TWO_DAY");
    expect(got.lines).toHaveLength(1);
  });

  it("encodes the assignment id into the path", async () => {
    const api = mockApi({ "GET /assignments/fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f": json(queuedAssignment()) });
    await getAssignment("fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f");
    expect(api.to("GET /assignments/fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f")).toHaveLength(1);
  });

  it("surfaces the problem slug of a 404", async () => {
    mockApi({
      "GET /assignments/fa-nope": problem(404, "not-found", "Not found", "No assignment has the id fa-nope."),
    });
    await expect(getAssignment("fa-nope")).rejects.toMatchObject({
      slug: "not-found",
      status: 404,
      title: "Not found",
    });
  });

  it("surfaces the problem slug of a 422", async () => {
    mockApi({
      "GET /assignments": problem(422, "validation-failed", "Validation failed", "limit must be 1..500."),
    });
    await expect(listAssignments({ limit: 9999 })).rejects.toMatchObject({
      slug: "validation-failed",
      status: 422,
    });
  });

  it("surfaces a non-JSON error as a plain ApiError", async () => {
    mockApi({
      "GET /assignments/fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f": new Response("gateway exploded", { status: 502 }),
    });
    await expect(getAssignment("fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f")).rejects.toMatchObject({ status: 502, slug: "" });
  });
});
