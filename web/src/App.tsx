import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { AssignmentListScreen } from "./screens/AssignmentListScreen";
import { AssignmentLookupScreen, AssignmentRoute } from "./screens/AssignmentScreen";

const SUB_NAV = [
  { to: "queue", label: "Virtual queue" },
  { to: "assignments", label: "Assignment lookup" },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/order-sourcing/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7
 *  uses the last path-contributing match's pathname, splat included), so from
 *  /order-sourcing/assignments the link `queue` would point at
 *  /order-sourcing/assignments/queue. A pathless layout route does not help
 *  (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the other fleet remotes;
 *  App.test.tsx mounts under the host's splat route to prove it.) */
function SourcingLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Order sourcing sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as order_sourcing_mfe/App via Module Federation. Takes NO props:
 *  the console mounts it under `/order-sourcing/*` and provides the
 *  BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`. Routes
 *  here are RELATIVE, so the component works identically under that prefix (in
 *  the shell) or at / (standalone dev, see main.tsx). It calls ONLY this
 *  context's own API (/api/order-sourcing), and it is read-only: it renders
 *  the two assignment reads and changes nothing (re-optimising an assignment
 *  is a REST operation of the API, not of this console).
 *
 *   - (index): redirects to the queue.
 *   - queue: the virtual queue, with the state/order/node filters.
 *   - assignments: an id lookup; assignments/:assignmentId: one in full. */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<SourcingLayout />}>
        <Route index element={<Navigate to="queue" replace />} />
        <Route path="queue" element={<AssignmentListScreen />} />
        <Route path="assignments" element={<AssignmentLookupScreen />} />
        <Route path="assignments/:assignmentId" element={<AssignmentRoute />} />
      </Route>
    </Routes>
  );
}
