import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AssignmentLookupScreen, AssignmentRoute } from "./AssignmentScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { queuedAssignment, releasedAssignment } from "../test/fixtures";

const ID = "fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/order-sourcing/*">
          <Route path="assignments" element={<AssignmentLookupScreen />} />
          <Route path="assignments/:assignmentId" element={<AssignmentRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("AssignmentLookupScreen", () => {
  it("navigates to the assignment on submit", async () => {
    mockApi({ [`GET /assignments/${ID}`]: json(queuedAssignment()) });
    renderAt("/order-sourcing/assignments");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/Assignment id/), ID);
    await user.click(screen.getByRole("button", { name: "Show assignment" }));
    expect(await screen.findByRole("heading", { name: "Assignment detail" })).toBeInTheDocument();
  });

  it("refuses a malformed id instead of calling the API", async () => {
    const api = mockApi({ "GET /assignments/bad/id": json({}) });
    renderAt("/order-sourcing/assignments");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/Assignment id/), "bad/id");
    expect(screen.getByRole("button", { name: "Show assignment" })).toBeDisabled();
    expect(screen.getByText("An assignment id cannot contain spaces, control characters or '/'.")).toBeInTheDocument();
    expect(api.calls).toHaveLength(0);
  });
});

describe("AssignmentScreen", () => {
  it("renders a QUEUED assignment with its candidate path and excluded nodes", async () => {
    mockApi({ [`GET /assignments/${ID}`]: json(queuedAssignment()) });
    renderAt(`/order-sourcing/assignments/${ID}`);
    expect(await screen.findByRole("heading", { name: "Assignment detail" })).toBeInTheDocument();
    expect(screen.getByText("Promise constraint")).toBeInTheDocument();
    expect(screen.getByText("Current candidate path")).toBeInTheDocument();
    expect(screen.getByText("node-214>hub-3 → hub-3>dst-30301")).toBeInTheDocument();
    expect(screen.getByText("130 pts")).toBeInTheDocument();
    expect(screen.getByText("Excluded nodes (1)")).toBeInTheDocument();
    expect(screen.getByText("no-capacity")).toBeInTheDocument();
    expect(screen.getByText("Not released yet: the release point is 2026-10-24 14:00:00Z.")).toBeInTheDocument();
    expect(screen.queryByText("Release history")).not.toBeInTheDocument();
  });

  it("renders a RELEASED assignment with its release history and lifecycle facts", async () => {
    mockApi({ [`GET /assignments/${ID}`]: json(releasedAssignment()) });
    renderAt(`/order-sourcing/assignments/${ID}`);
    expect(await screen.findByText("Release history (1)")).toBeInTheDocument();
    expect(screen.getAllByText("node-214").length).toBeGreaterThan(0);
    expect(screen.getByText("PENDING")).toBeInTheDocument();
    expect(screen.queryByText("Current candidate path")).not.toBeInTheDocument();
    expect(screen.getByText("Lifecycle facts")).toBeInTheDocument();
  });

  it("renders the problem of an unknown id", async () => {
    mockApi({
      [`GET /assignments/${ID}`]: problem(404, "not-found", "Not found", `No assignment has the id ${ID}.`),
    });
    renderAt(`/order-sourcing/assignments/${ID}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Not found");
    expect(alert).toHaveTextContent("No assignment has the id");
  });
});
