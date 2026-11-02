---
id: tools
title: MCP tools
sidebar_label: MCP tools
description: "The two read-only MCP tools of Order Sourcing (list_assignments, get_assignment): inputs, outputs, annotations and error behaviour."
---

# MCP tools

`cmd/mcp` serves the Model Context Protocol over **Streamable HTTP only** ([ADR 0006](/docs/adr/0006-mcp-server-adoption)): the
endpoint is `/mcp` (port `MCP_ADDR`, default `:8090`; behind Kong `/api/mcp/order-sourcing`, rewritten to `/mcp`) and `/healthz`
answers liveness. The root `/` also reaches the MCP handler. The server announces itself as `order-sourcing-mcp` version `1.0.0`.
It needs only `DATABASE_URL`: no Kafka, no relay, no consumer. There is **no authentication**, by fleet decision, and no tool
that writes: `TestToolSurface` pins the exact set and fails the build on a write-verb name. The adapter's `Deps` carries
`usecases.Queries` only, and `TestMCPAdapterDependencyRule` keeps it off every outbound adapter. Re-optimising an assignment
stays a REST operation
(`POST /assignments/{id}:reoptimise`); every other lifecycle change arrives as a Kafka event.

Source: `internal/adapters/inbound/mcp/server.go`, `tools.go`, `errors.go`, `cmd/mcp/router.go`, and the registry pinned in
`internal/adapters/inbound/mcp/testdata/tool_registry.golden.json` (names, descriptions, annotations, input and output
schemas, compared byte for byte by `TestToolRegistryGolden`). This page was written from that code and golden file, not from a
captured session.

## Tool list

| Tool | Read-only | Idempotent | Open world | Same read as |
| --- | --- | --- | --- | --- |
| `list_assignments` | yes (`readOnlyHint`) | yes | no | `GET /assignments`, A2A `find_assignments` |
| `get_assignment` | yes (`readOnlyHint`) | yes | no | `GET /assignments/{id}`, A2A `assignment_detail` |

Both tools call the same `usecases.Queries` as the REST handlers, so the data, the ordering and the validation are identical.

## `list_assignments`

Lists fulfillment assignments ordered by `assignmentId` ascending: which vendor node and path will fulfil each shipment group of
a customer order, and in which state. Filters combine with AND; every input is optional (the registry has no `required` list for
this tool, pinned by `TestOptionalInputsAreNotRequired`).

| Input | Type | Required | Meaning |
| --- | --- | --- | --- |
| `state` | string | no | One of `QUEUED`, `RELEASED`, `LOCKED`, `SHIPPED`, `DELIVERED`, `UNSOURCEABLE`, `CANCELLED`; omitted means every state |
| `nodeId` | string | no | Keep assignments currently on this node: the candidate's node while `QUEUED`, the node of the latest release afterwards |
| `orderId` | string | no | Keep assignments of this order |
| `limit` | integer | no | Page size 1 to 500; omitted or 0 means 100 |

Output: `{"assignments": [ ... ]}`, each item an assignment (shape below). An empty match is an empty array. An unknown `state`
or a `limit` outside 1 to 500 fails with `validation-failed`. There is no cursor: the list is capped by `limit`.

## `get_assignment`

Reads one assignment in full.

| Input | Type | Required | Meaning |
| --- | --- | --- | --- |
| `assignmentId` | string | yes | The assignment id |

Output: one assignment. An unknown id fails with `not-found`.

## The assignment shape

The same camelCase body as the REST `Assignment` schema (`apis/openapi.yaml`):

| Field | Meaning |
| --- | --- |
| `assignmentId`, `orderId`, `groupNo` | Identity: one assignment per `(orderId, groupNo)` |
| `destinationPostalPrefix` | The postal prefix of the destination, never an address |
| `state` | One of the seven states |
| `assignmentVersion` | The optimistic-concurrency version |
| `promise` | `promiseId`, `tier` (`SAME_DAY`, `NEXT_DAY`, `TWO_DAY`, `STANDARD`), `deliverByLatest`, `witnessNodeId`, `revisionNo` |
| `lines[]` | `lineNo`, `catalogItemId`, `listingId` (only from the first release), `quantity` |
| `candidate` | The current best node and path: `nodeId`, `pickupDepartureId`, `pickupCutoffAt`, `legs[]`, `deliveryWaveId`, `eta`, `cost`; absent when `UNSOURCEABLE` or `CANCELLED` |
| `releaseAt` | The virtual-queue release point, while `QUEUED` |
| `releases[]` | `releaseNo`, `nodeId`, `releasedAt`, `requiredShipBy`, `outcome` (`PENDING`, `ACCEPTED`, `REJECTED`, `EXPIRED`), `reasonCode` |
| `excludedNodes[]` | `nodeId`, `reason` for every node that refused the current promise revision |
| `lockedAt`, `lockReason`, `pickupDepartureId` | The lock facts |
| `trackingNumbers[]`, `shippedAt`, `deliveredAt`, `cancelReason` | Shipment, delivery and cancel facts when they apply |

Timestamps are RFC 3339 UTC strings; unset optional fields are omitted. No PII is held by this context.

## Errors

A tool failure is returned as an MCP tool result with `isError` set and a text of the form `<slug>: <detail>` (never a protocol
error), using the REST problem slugs: `not-found`, `validation-failed`. Any other failure is logged with the trace id and reported
as `internal-error: an unexpected internal error occurred`, so DSNs and SQL never reach the model.

## Trying it

The first calls are on the [Quickstart](/docs/overview/quickstart#mcp). A raw JSON-RPC request needs both accept types
(`Accept: application/json, text/event-stream`); an MCP SDK client is easier. The server instructions announced at `initialize`
say the surface is read-only and name both tools.

The sibling read surface for agents is A2A, described on the [API overview](/docs/api-reference#a2a).
