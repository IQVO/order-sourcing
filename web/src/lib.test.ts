import { describe, expect, it } from "vitest";
import { assignmentIdProblem, costLabel, currentNode, idLabel, orderIdProblem, parseState, utcLabel } from "./lib";
import { releasedAssignment } from "./test/fixtures";

describe("utcLabel", () => {
  it("formats an RFC 3339 instant as UTC seconds", () => {
    expect(utcLabel("2026-10-24T14:03:00Z")).toBe("2026-10-24 14:03:00Z");
  });

  it("keeps the raw text when unparseable and a placeholder when absent", () => {
    expect(utcLabel("not a date")).toBe("not a date");
    expect(utcLabel(undefined)).not.toBe("");
  });
});

describe("costLabel", () => {
  it("renders a path cost in cost points", () => {
    expect(costLabel(130)).toBe("130 pts");
    expect(costLabel(0)).toBe("0 pts");
  });
});

describe("idLabel", () => {
  it("keeps a short id whole and shortens a long one at both ends", () => {
    expect(idLabel("fa-short")).toBe("fa-short");
    expect(idLabel("fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f")).toBe("fa-1b4e28…4e5f");
  });
});

describe("parseState", () => {
  it("accepts every lifecycle state", () => {
    expect(parseState("QUEUED")).toBe("QUEUED");
    expect(parseState("UNSOURCEABLE")).toBe("UNSOURCEABLE");
  });

  it("rejects anything else (the contract's validation-failed set)", () => {
    expect(parseState("queued")).toBeNull();
    expect(parseState("BOGUS")).toBeNull();
    expect(parseState("")).toBeNull();
  });
});

describe("assignmentIdProblem / orderIdProblem", () => {
  it("accepts a well-formed id", () => {
    expect(assignmentIdProblem("fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f")).toBeNull();
    expect(orderIdProblem("ord-2026-0001")).toBeNull();
  });

  it("rejects an empty, overlong or malformed id the way the domain does", () => {
    expect(assignmentIdProblem("")).toMatch(/Enter an assignment id/);
    expect(assignmentIdProblem("x".repeat(65))).toMatch(/at most 64 characters/);
    expect(assignmentIdProblem("fa 1")).toMatch(/cannot contain spaces/);
    expect(assignmentIdProblem("fa/1")).toMatch(/cannot contain spaces/);
    expect(orderIdProblem("ord 1")).toMatch(/cannot contain spaces/);
  });
});

describe("currentNode", () => {
  it("is the candidate's node while QUEUED", () => {
    expect(currentNode(releasedAssignment({ state: "QUEUED", candidate: { nodeId: "node-9", pickupDepartureId: "d", pickupCutoffAt: "2026-10-24T17:00:00Z", legs: [], deliveryWaveId: "w", eta: "2026-10-25T09:30:00Z", cost: 1 }, releases: [] }))).toBe("node-9");
  });

  it("is the node of the latest release from RELEASED on", () => {
    const a = releasedAssignment({
      candidate: undefined,
      releases: [
        { releaseNo: 1, nodeId: "node-1", releasedAt: "2026-10-24T14:00:00Z", requiredShipBy: "2026-10-24T17:00:00Z", outcome: "REJECTED" },
        { releaseNo: 2, nodeId: "node-2", releasedAt: "2026-10-24T15:00:00Z", requiredShipBy: "2026-10-24T18:00:00Z", outcome: "PENDING" },
      ],
    });
    expect(currentNode(a)).toBe("node-2");
  });

  it("is empty when neither exists", () => {
    expect(currentNode({ candidate: undefined, releases: [] })).toBe("");
  });
});
