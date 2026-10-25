import { describe, expect, it } from "vitest";
import { resolveSourcingApiBase } from "./config";

describe("resolveSourcingApiBase", () => {
  it("is this context's own /api/order-sourcing under the runtime origin", () => {
    expect(resolveSourcingApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/order-sourcing",
    );
  });

  it("drops trailing slashes of the origin", () => {
    expect(resolveSourcingApiBase({ apiOrigin: "http://kong.local///" }, false)).toBe(
      "http://kong.local/api/order-sourcing",
    );
  });

  it("falls back to the developer port outside production", () => {
    expect(resolveSourcingApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("refuses to guess in production", () => {
    expect(() => resolveSourcingApiBase({}, true)).toThrow(/apiOrigin is required/);
    expect(() => resolveSourcingApiBase({ apiOrigin: "" }, true)).toThrow(/apiOrigin is required/);
  });
});
