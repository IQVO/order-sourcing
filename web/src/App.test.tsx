import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { assignmentPage, queuedAssignment } from "./test/fixtures";

// The console mounts the remote under its own splat route; the remote's
// relative links must resolve against the mount point, not the full URL.
function renderUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/order-sourcing/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const ID = "fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f";

const ROUTES = {
  "GET /assignments": json(assignmentPage()),
  [`GET /assignments/${ID}`]: json(queuedAssignment()),
};

describe("App", () => {
  it("redirects the mount point to the virtual queue", async () => {
    mockApi(ROUTES);
    renderUnderHost("/order-sourcing");
    expect(await screen.findByRole("heading", { name: "Fulfillment assignments" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Virtual queue" })).toHaveAttribute(
      "href",
      "/order-sourcing/queue",
    );
  });

  it("keeps the sub-nav at the mount point from a deep route", async () => {
    mockApi(ROUTES);
    renderUnderHost(`/order-sourcing/assignments/${ID}`);
    await screen.findByRole("heading", { name: "Assignment detail" });
    expect(screen.getByRole("link", { name: "Assignment lookup" })).toHaveAttribute(
      "href",
      "/order-sourcing/assignments",
    );
  });

  it("navigates from the queue into an assignment and back", async () => {
    mockApi(ROUTES);
    renderUnderHost("/order-sourcing/queue");
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole("link", { name: /fa-1b4e2/ }))[0]);
    await screen.findByRole("heading", { name: "Assignment detail" });
    await user.click(screen.getByRole("link", { name: "← Assignment lookup" }));
    expect(await screen.findByRole("heading", { name: "Look up an assignment" })).toBeInTheDocument();
  });
});
