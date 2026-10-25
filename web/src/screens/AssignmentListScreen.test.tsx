import { MemoryRouter } from "react-router-dom";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { AssignmentListScreen } from "./AssignmentListScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { assignmentPage } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter>
      <AssignmentListScreen />
    </MemoryRouter>,
  );
}

describe("AssignmentListScreen", () => {
  it("loads the queue and renders one row per assignment", async () => {
    const api = mockApi({ "GET /assignments": json(assignmentPage()) });
    renderScreen();
    expect(await screen.findAllByRole("link", { name: /fa-1b4e2/ })).toHaveLength(1);
    expect(await screen.findAllByRole("link", { name: /fa-2c5f3/ })).toHaveLength(1);
    expect(screen.getByRole("heading", { name: "Fulfillment assignments" })).toBeInTheDocument();
    expect(screen.getByText("130 pts")).toBeInTheDocument();
    expect(screen.getAllByText("node-214").length).toBe(2);
    expect(screen.getAllByText("TWO_DAY").length).toBe(2);
    const call = api.to("GET /assignments")[0];
    expect(call.query.get("limit")).toBe("25");
    expect(call.query.get("state")).toBeNull();
    expect(api.calls.every((c) => c.method === "GET")).toBe(true);
  });

  it("applies the state and order filters only on submit", async () => {
    const api = mockApi({ "GET /assignments": json(assignmentPage()) });
    renderScreen();
    await screen.findByText("130 pts");
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("State"), "QUEUED");
    await user.type(screen.getByLabelText(/Order id/), "ord-2026-0001");
    expect(api.to("GET /assignments")).toHaveLength(1); // not yet: filters are a form
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.to("GET /assignments")).toHaveLength(2));
    const call = api.to("GET /assignments")[1];
    expect(call.query.get("state")).toBe("QUEUED");
    expect(call.query.get("orderId")).toBe("ord-2026-0001");
  });

  it("renders the empty state of a page with no match", async () => {
    mockApi({ "GET /assignments": json({ assignments: [] }) });
    renderScreen();
    expect(await screen.findByText("No assignment matches these filters.")).toBeInTheDocument();
  });

  it("renders the problem of a failed page", async () => {
    mockApi({
      "GET /assignments": problem(
        422,
        "validation-failed",
        "Validation failed",
        "state must be one of QUEUED, RELEASED, LOCKED, SHIPPED, DELIVERED, UNSOURCEABLE, CANCELLED.",
      ),
    });
    renderScreen();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Validation failed");
    expect(alert).toHaveTextContent("state must be one of");
  });
});
