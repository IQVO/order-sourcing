import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/order-sourcing-api",
    },
    {
      type: "category",
      label: "Assignments",
      link: {
        type: "doc",
        id: "api-reference/rest/assignments",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-assignments",
          label: "List fulfillment assignments (ListAssignments)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-assignment",
          label: "Get one fulfillment assignment (GetAssignment)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/reoptimise-assignment",
          label: "Re-optimise a queued assignment now (ReoptimiseAssignment)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Sourcing policy",
      link: {
        type: "doc",
        id: "api-reference/rest/sourcing-policy",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-sourcing-policy",
          label: "Get the current sourcing policy (GetSourcingPolicy)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/put-sourcing-policy",
          label: "Publish the next sourcing policy version (PublishSourcingPolicy)",
          className: "api-method put",
        },
      ],
    },
    {
      type: "category",
      label: "reports",
      link: {
        type: "doc",
        id: "api-reference/rest/reports",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-sourcing-mix-report",
          label: "Sourcing mix (in-region share, legs, reassignments), per day",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-order-splits-report",
          label: "Order splits (groups per order), per day",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-cost-to-serve-report",
          label: "Cost to serve per unit, per day",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-report-freshness",
          label: "How far the analytics projection is behind",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
