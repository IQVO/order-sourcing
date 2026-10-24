/**
 * Runtime endpoint resolution for order_sourcing_mfe.
 *
 * This remote is deployed as one image that must work in more than one
 * environment, so the API location cannot be a build-time constant. The
 * console shell publishes `window.__WAREHOUSE_CONFIG__` from a runtime
 * /config.json before any remote mounts; this module turns that origin into
 * this context's own API base: `${apiOrigin}/api/order-sourcing`. It is
 * the ONLY base this remote ever calls (no sibling context, no other
 * service).
 *
 * The fleet's localhost topology puts APIs on a DIFFERENT origin from the
 * frontend (Kong on :8000, Nginx on :80), so this is a real cross-origin URL
 * rather than a same-origin path -- the gateway carries the matching CORS
 * policy. Every call this remote makes is a GET with no custom header, so
 * they stay simple CORS requests.
 *
 * In a production build a missing/malformed origin throws rather than falling
 * back to a developer port: a silent fallback would mean a deployed console
 * quietly talking to nothing.
 */
export interface WarehouseRuntimeConfig {
  apiOrigin?: string;
}

declare global {
  interface Window {
    __WAREHOUSE_CONFIG__?: WarehouseRuntimeConfig;
  }
}

const API_PATH = "/api/order-sourcing";
// cmd/api's own default HTTP_ADDR; only ever used by `npm run dev`.
const DEV_API_BASE = "http://localhost:8080";

export function resolveSourcingApiBase(
  runtimeConfig: WarehouseRuntimeConfig,
  isProduction: boolean,
): string {
  const apiOrigin = runtimeConfig.apiOrigin?.replace(/\/+$/, "");
  if (!apiOrigin) {
    if (isProduction) {
      throw new Error("window.__WAREHOUSE_CONFIG__.apiOrigin is required in production");
    }
    return DEV_API_BASE;
  }
  return `${apiOrigin}${API_PATH}`;
}

export const SOURCING_API_BASE = resolveSourcingApiBase(
  typeof window === "undefined" ? {} : (window.__WAREHOUSE_CONFIG__ ?? {}),
  import.meta.env.PROD,
);
