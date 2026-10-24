import type { ReactNode } from "react";
import { StatusPill } from "@warehouse/ui-kit";
import type { AssignmentState, Outcome } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** A state's colour: pre-release is in flight, LOCKED..DELIVERED went well,
 *  UNSOURCEABLE/CANCELLED are the failure ends. */
const STATE_TONE: Record<AssignmentState, Tone> = {
  QUEUED: "neutral",
  RELEASED: "progress",
  LOCKED: "progress",
  SHIPPED: "progress",
  DELIVERED: "success",
  UNSOURCEABLE: "warning",
  CANCELLED: "danger",
};

export function StatePill({ state }: { state: AssignmentState }) {
  return <StatusPill status={state} tone={STATE_TONE[state]} size="sm" />;
}

/** A release outcome's colour: PENDING is still open, ACCEPTED went through,
 *  REJECTED/EXPIRED are the node's refusals. */
const OUTCOME_TONE: Record<Outcome, Tone> = {
  PENDING: "neutral",
  ACCEPTED: "success",
  REJECTED: "danger",
  EXPIRED: "warning",
};

export function OutcomePill({ outcome }: { outcome: Outcome }) {
  return <StatusPill status={outcome} tone={OUTCOME_TONE[outcome]} size="sm" />;
}
