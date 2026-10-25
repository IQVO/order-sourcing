import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { ApiError, getAssignment } from "../api";
import { Facts, OutcomePill, PageHeader, Stack, StatePill } from "../components/layout";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { assignmentIdProblem, costLabel, currentNode, utcLabel } from "../lib";

/** The route element of `assignments` (the lookup). */
export function AssignmentLookupScreen() {
  const [assignmentId, setAssignmentId] = useState("");
  const navigate = useNavigate();
  const problem = assignmentIdProblem(assignmentId.trim());

  return (
    <Stack gap={5}>
      <PageHeader
        title="Look up an assignment"
        subtitle="order-sourcing · one shipment group in full: its promise, candidate path, release history and lock facts"
      />
      <Card title="Assignment id">
        <Form
          label="Assignment lookup"
          onSubmit={() => {
            // Relative like every link here: ".." is the layout route (this
            // remote's mount point), so the detail URL is right under the
            // console's /order-sourcing/* and standalone at /.
            if (!problem) navigate(`../assignments/${encodeURIComponent(assignmentId.trim())}`);
          }}
        >
          <FormRow>
            <TextField
              label="Assignment id"
              value={assignmentId}
              onChange={setAssignmentId}
              placeholder="fa-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"
              required
              hint={problem ?? "The id the context issued when the order was placed"}
            />
            <SubmitButton disabled={problem !== null}>Show assignment</SubmitButton>
          </FormRow>
        </Form>
      </Card>
    </Stack>
  );
}

/** The route element of `assignments/:assignmentId`. */
export function AssignmentRoute() {
  const { assignmentId } = useParams();
  return assignmentId ? <AssignmentScreen assignmentId={assignmentId} /> : null;
}

/**
 * One assignment in full: GET /assignments/{assignmentId} -- the promise
 * constraint, the lines, the current candidate path with its cost, the
 * VirtualQueue release history with each outcome, the excluded nodes and the
 * lock/shipment facts once they apply. An unknown id answers not-found.
 */
export function AssignmentScreen({ assignmentId }: { assignmentId: string }) {
  const assignment = useRequest(() => getAssignment(assignmentId), `assignment|${assignmentId}`);
  const problem = assignmentIdProblem(assignmentId);
  return (
    <Stack gap={5}>
      <PageHeader title="Assignment detail" subtitle="Everything the sourcing context holds about this shipment group" />
      {problem ? (
        <ProblemAlert error={new ApiError(0, { detail: problem }, "Invalid assignment id")} />
      ) : (
        <RequestView state={assignment} what="the assignment">
          {(a) => <AssignmentBody assignment={a} />}
        </RequestView>
      )}
      <Link to="../assignments">← Assignment lookup</Link>
    </Stack>
  );
}

function AssignmentBody({ assignment }: { assignment: import("../types").Assignment }) {
  const a = assignment;
  return (
    <>
      <Card title={`Assignment ${a.assignmentId}`}>
        <Facts
          items={[
            { label: "Assignment", value: a.assignmentId },
            { label: "Order", value: `${a.orderId} · group ${a.groupNo}` },
            { label: "Destination", value: a.destinationPostalPrefix },
            { label: "State", value: <StatePill state={a.state} /> },
            { label: "Version", value: String(a.assignmentVersion) },
            { label: "Current node", value: currentNode(a) || "—" },
          ]}
        />
      </Card>

      <Card title="Promise constraint">
        <Facts
          items={[
            { label: "Promise", value: a.promise.promiseId },
            { label: "Tier", value: a.promise.tier },
            { label: "Deliver by (latest)", value: utcLabel(a.promise.deliverByLatest) },
            { label: "Witness node", value: a.promise.witnessNodeId },
            { label: "Revision", value: String(a.promise.revisionNo) },
          ]}
        />
      </Card>

      <Card title="Lines">
        <DataTable
          rowKey={(l) => String(l.lineNo)}
          rows={a.lines}
          columns={[
            { key: "lineNo", header: "Line", align: "right", render: (l) => String(l.lineNo) },
            { key: "catalogItem", header: "Catalog item", render: (l) => l.catalogItemId },
            { key: "listing", header: "Listing", render: (l) => l.listingId ?? "—" },
            { key: "quantity", header: "Quantity", align: "right", render: (l) => String(l.quantity) },
          ]}
        />
      </Card>

      {a.candidate ? (
        <Card title="Current candidate path">
          <Facts
            items={[
              { label: "Node", value: a.candidate.nodeId },
              { label: "Pickup departure", value: a.candidate.pickupDepartureId },
              { label: "Pickup cutoff", value: utcLabel(a.candidate.pickupCutoffAt) },
              { label: "Legs", value: a.candidate.legs.join(" → ") },
              { label: "Delivery wave", value: a.candidate.deliveryWaveId },
              { label: "ETA", value: utcLabel(a.candidate.eta) },
              { label: "Cost", value: costLabel(a.candidate.cost) },
            ]}
          />
        </Card>
      ) : null}

      <Card title={`Release history (${a.releases.length})`}>
        {a.releases.length === 0 ? (
          <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)" }}>
            Not released yet: the release point is {utcLabel(a.releaseAt)}.
          </div>
        ) : (
          <DataTable
            rowKey={(r) => String(r.releaseNo)}
            rows={a.releases}
            columns={[
              { key: "releaseNo", header: "Release", align: "right", render: (r) => String(r.releaseNo) },
              { key: "node", header: "Node", render: (r) => r.nodeId },
              { key: "releasedAt", header: "Released at", render: (r) => utcLabel(r.releasedAt) },
              { key: "requiredShipBy", header: "Required ship-by", render: (r) => utcLabel(r.requiredShipBy) },
              { key: "outcome", header: "Outcome", render: (r) => <OutcomePill outcome={r.outcome} /> },
              { key: "reason", header: "Reason", render: (r) => r.reasonCode ?? "—" },
            ]}
          />
        )}
      </Card>

      {a.excludedNodes.length > 0 ? (
        <Card title={`Excluded nodes (${a.excludedNodes.length})`}>
          <DataTable
            rowKey={(e) => e.nodeId}
            rows={a.excludedNodes}
            columns={[
              { key: "node", header: "Node", render: (e) => e.nodeId },
              { key: "reason", header: "Reason", render: (e) => e.reason },
            ]}
          />
        </Card>
      ) : null}

      <Card title="Lifecycle facts">
        <Facts
          items={[
            { label: "State", value: <StatePill state={a.state} /> },
            { label: "Release point", value: utcLabel(a.releaseAt) },
            { label: "Locked at", value: utcLabel(a.lockedAt) },
            { label: "Lock reason", value: a.lockReason ?? "—" },
            { label: "Pickup departure", value: a.pickupDepartureId ?? "—" },
            { label: "Tracking numbers", value: a.trackingNumbers.length > 0 ? a.trackingNumbers.join(", ") : "—" },
            { label: "Shipped at", value: utcLabel(a.shippedAt) },
            { label: "Delivered at", value: utcLabel(a.deliveredAt) },
            { label: "Cancel reason", value: a.cancelReason ?? "—" },
          ]}
        />
      </Card>
    </>
  );
}
