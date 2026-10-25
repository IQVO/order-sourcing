import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listAssignments } from "../api";
import { Form, FormRow, SelectField, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { costLabel, currentNode, idLabel, orderIdProblem, utcLabel } from "../lib";
import { STATES } from "../types";
import type { Assignment, AssignmentState } from "../types";

const STATE_OPTIONS = STATES.map((s) => ({ value: s, label: s }));

type Filters = { state: "" | AssignmentState; orderId: string; nodeId: string };

/**
 * The virtual queue: GET /assignments?state=&orderId=&nodeId=, ordered by
 * assignmentId ascending, one page at a time (the contract caps a page at
 * limit, default 100; this screen asks for 25 and re-issues the query when a
 * filter changes). The three filters combine with AND; nodeId matches the
 * current candidate node (QUEUED) or the node of the latest release later.
 */
export function AssignmentListScreen() {
  const [draft, setDraft] = useState<Filters>({ state: "", orderId: "", nodeId: "" });
  const [applied, setApplied] = useState<Filters>({ state: "", orderId: "", nodeId: "" });

  // An empty order id is no filter at all, not a validation failure; only a
  // NON-empty malformed one blocks the form.
  const trimmedOrder = draft.orderId.trim();
  const problem = trimmedOrder === "" ? null : orderIdProblem(trimmedOrder);
  const page = useRequest(
    problem ? null : () =>
      listAssignments({
        state: applied.state || undefined,
        orderId: applied.orderId.trim() || undefined,
        nodeId: applied.nodeId.trim() || undefined,
        limit: 25,
      }),
    `assignments|${applied.state}|${applied.orderId}|${applied.nodeId}`,
  );

  const apply = () => setApplied({ state: draft.state, orderId: draft.orderId.trim(), nodeId: draft.nodeId.trim() });

  return (
    <Stack gap={5}>
      <PageHeader
        title="Fulfillment assignments"
        subtitle="order-sourcing · which vendor node and path will fulfil each shipment group of a customer order"
      />
      <Card title="Filters">
        <Form label="Assignment filters" onSubmit={apply}>
          <FormRow>
            <SelectField
              label="State"
              value={draft.state}
              onChange={(v) => setDraft({ ...draft, state: v as "" | AssignmentState })}
              options={STATE_OPTIONS}
              emptyLabel="Every state"
            />
            <TextField
              label="Order id"
              value={draft.orderId}
              onChange={(v) => setDraft({ ...draft, orderId: v })}
              placeholder="ord-2026-0001"
              hint={problem ?? "Only assignments of this order"}
            />
            <TextField
              label="Node id"
              value={draft.nodeId}
              onChange={(v) => setDraft({ ...draft, nodeId: v })}
              placeholder="node-214"
              hint="The current candidate node (QUEUED) or the node of the latest release"
            />
            <SubmitButton disabled={problem !== null}>Apply filters</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <Card title={applied.state || "All states"}>
        <RequestView
          state={page}
          what="assignments"
          isEmpty={(p) => p.assignments.length === 0}
          empty="No assignment matches these filters."
        >
          {(p) => <AssignmentTable assignments={p.assignments} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function AssignmentTable({ assignments }: { assignments: Assignment[] }) {
  return (
    <DataTable
      rowKey={(a) => a.assignmentId}
      rows={assignments}
      columns={[
        {
          key: "assignment",
          header: "Assignment",
          // Relative link: `..` is the parent route (this remote's mount
          // point), so it works under the console's /order-sourcing/* and
          // standalone at /.
          render: (a) => (
            <Link to={`../assignments/${encodeURIComponent(a.assignmentId)}`}>{idLabel(a.assignmentId)}</Link>
          ),
        },
        { key: "order", header: "Order", render: (a) => `${a.orderId} · group ${a.groupNo}` },
        { key: "destination", header: "Destination", render: (a) => a.destinationPostalPrefix },
        { key: "state", header: "State", render: (a) => <StatePill state={a.state} /> },
        { key: "node", header: "Current node", render: (a) => currentNode(a) || "—" },
        {
          key: "candidateCost",
          header: "Candidate cost",
          align: "right",
          render: (a) => (a.candidate ? costLabel(a.candidate.cost) : "—"),
        },
        {
          key: "promise",
          header: "Promise",
          render: (a) => (
            <span>
              {a.promise.tier} <span style={{ color: "var(--wh-color-text-muted)" }}>by {utcLabel(a.promise.deliverByLatest)}</span>
            </span>
          ),
        },
        { key: "releaseAt", header: "Release point", render: (a) => utcLabel(a.releaseAt) },
      ]}
    />
  );
}
