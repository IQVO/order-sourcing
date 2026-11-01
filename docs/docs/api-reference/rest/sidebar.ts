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
  ],
};

export default sidebar.apisidebar;
