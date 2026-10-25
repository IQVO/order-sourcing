#!/usr/bin/env python3
"""Assert this chart's Service selector isolates its one component.

Why this exists (fleet-wide): `app.kubernetes.io/name` + `instance` are identical
on every pod a release creates. A Service that selects on those two alone would
select every component the day a second one is added. The Deployment here carries
`app.kubernetes.io/component: api` in selector.matchLabels and its pod labels, and
the Service selects on it, so each Service selects EXACTLY ONE Deployment. The
chart has one component (api), so this runs with every optional object (HPA,
Ingress, HTTPRoute) enabled and with the defaults, and also asserts the api
env/refusal contract that does not depend on grep.

Run: python3 charts/order-sourcing/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "order-sourcing"

# Dummy values, no password: nothing here is ever dialled.
GROUPS = {
    "customerOrderingConsumerGroup": "order-sourcing-customer-ordering",
    "deliveryPromiseConsumerGroup": "order-sourcing-delivery-promise",
    "lastMileDeliveryConsumerGroup": "order-sourcing-last-mile-delivery",
    "linehaulTransportationConsumerGroup": "order-sourcing-linehaul-transportation",
    "nodePerformanceConsumerGroup": "order-sourcing-node-performance",
    "nodeTopologyConsumerGroup": "order-sourcing-node-topology",
    "offerAvailabilityConsumerGroup": "order-sourcing-offer-availability",
    "shippingAndTrackingConsumerGroup": "order-sourcing-shipping-and-tracking",
    "vendorConnectivityConsumerGroup": "order-sourcing-vendor-connectivity",
}

# Every env var cmd/api reads, as the chart renders it.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "EVENT_PUBLISHER", "KAFKA_BROKERS", "DATABASE_URL", "MIGRATIONS_DATABASE_URL",
    "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY",
    "CUSTOMER_ORDERING_CONSUMER_GROUP",
    "DELIVERY_PROMISE_CONSUMER_GROUP",
    "LAST_MILE_DELIVERY_CONSUMER_GROUP",
    "LINEHAUL_TRANSPORTATION_CONSUMER_GROUP",
    "NODE_PERFORMANCE_CONSUMER_GROUP",
    "NODE_TOPOLOGY_CONSUMER_GROUP",
    "OFFER_AVAILABILITY_CONSUMER_GROUP",
    "SHIPPING_AND_TRACKING_CONSUMER_GROUP",
    "VENDOR_CONNECTIVITY_CONSUMER_GROUP",
)


def runtime(skip: str | None = None) -> list[str]:
    """The mandatory values, minus the one named by skip (database, kafka or a group key)."""
    args: list[str] = []
    if skip != "database":
        args += ["--set", "database.url=postgres://u@example.invalid:5432/db"]
    if skip != "kafka":
        args += ["--set", "kafka.brokers=kafka.example.invalid:9092"]
    for key, value in GROUPS.items():
        if skip != key:
            args += ["--set", f"config.{key}={value}"]
    return args


# Every env var cmd/mcp reads (cmd/mcp/main.go).
MCP_ENV = ("MCP_ADDR", "LOG_LEVEL", "SERVICE_VERSION", "ENVIRONMENT", "DATABASE_URL", "MIGRATIONS_DATABASE_URL")

BASE = runtime()
ENABLE_EVERYTHING = BASE + [
    "--set", "autoscaling.api.enabled=true",
    "--set", "mcp.enabled=true",
    "--set", "frontend.enabled=true",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    "--set", "config.outboxRelayInterval=2s",
    "--set", "config.sweepInterval=30s",
]


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in env_of(dep)]


def env_value(dep: dict, name: str) -> str | None:
    return next((e.get("value") for e in env_of(dep) if e["name"] == name), None)


def check_components(docs: list[dict], failures: list[str], expect: list[str] | None = None) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}
    want = sorted(expect) if expect is not None else [RELEASE]

    if sorted(deployments) != want:
        failures.append(f"expected Deployments {want}, got {sorted(deployments)}")
    if sorted(services) != want:
        failures.append(f"expected Services {want}, got {sorted(services)}")
    if RELEASE not in services:
        failures.append("the api Service was not rendered")
    elif selector_of(services[RELEASE]).get("app.kubernetes.io/component") != "api":
        failures.append("the api Service selector must pin component=api")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # The HPA scales the api Deployment and owns its replicas.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in deployments.get(RELEASE, {}).get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    # The Ingress and HTTPRoute route to the api Service only.
    for d in docs:
        if d.get("kind") == "Ingress":
            for rule in d["spec"]["rules"]:
                for p in rule["http"]["paths"]:
                    if p["backend"]["service"]["name"] != RELEASE:
                        failures.append(f"Ingress routes to {p['backend']['service']['name']}, not the api Service")
        if d.get("kind") == "HTTPRoute":
            for rule in d["spec"]["rules"]:
                for ref in rule["backendRefs"]:
                    if ref["name"] != RELEASE:
                        failures.append(f"HTTPRoute routes to {ref['name']}, not the api Service")

    api = deployments.get(RELEASE)
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    api_env = env_names(api)
    for want in API_ENV:
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    modes = [n for n in api_env if n.endswith("_MODE")]
    if modes:
        failures.append(f"the api Deployment renders *_MODE variables: {modes}")
    for name, want in (("OUTBOX_RELAY_INTERVAL", "2s"), ("SWEEP_INTERVAL", "30s"), ("EVENT_PUBLISHER", "kafka")):
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")
    for key, want in GROUPS.items():
        name = "".join("_" + c if c.isupper() else c.upper() for c in key[: -len("ConsumerGroup")]).lstrip("_") + "_CONSUMER_GROUP"
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")


def command_of(dep: dict) -> list[str]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("command", [])


def pins(svc: dict | None, component: str) -> bool:
    if svc is None:
        return False
    return selector_of(svc).get("app.kubernetes.io/component") == component


def check_mcp(deployments: dict, services: dict, failures: list[str]) -> None:
    name = f"{RELEASE}-mcp"
    if not pins(services.get(name), "mcp"):
        failures.append("the MCP Service must exist and pin component=mcp")
    if name not in deployments:
        failures.append("the MCP Deployment was not rendered with mcp.enabled=true")
        return
    if command_of(deployments[name]) != ["/app/mcp"]:
        failures.append("the MCP Deployment must run /app/mcp")
    env = env_names(deployments[name])
    for want in MCP_ENV:
        if want not in env:
            failures.append(f"the MCP Deployment does not render {want}")
    for banned in ("KAFKA_BROKERS", "EVENT_PUBLISHER", "OUTBOX_RELAY_INTERVAL", "SWEEP_INTERVAL"):
        if banned in env:
            failures.append(f"the MCP Deployment must not render {banned} (read-only, never dials Kafka)")


def check_frontend(docs: list[dict], deployments: dict, services: dict, failures: list[str]) -> None:
    name = f"{RELEASE}-frontend"
    if not pins(services.get(name), "frontend"):
        failures.append("the frontend Service must exist and pin component=frontend")
    if services.get(name, {}).get("spec", {}).get("type") != "ClusterIP":
        failures.append("the frontend Service must be ClusterIP")
    if name not in deployments:
        failures.append("the frontend Deployment was not rendered with frontend.enabled=true")
        return
    env = env_names(deployments[name])
    for banned in ("DATABASE_URL", "KAFKA_BROKERS"):
        if banned in env:
            failures.append(f"the frontend Deployment must not render {banned} (it serves static bytes)")
    for d in docs:
        if d.get("kind") in {"Ingress", "HTTPRoute"} and "frontend" in d["metadata"]["name"]:
            failures.append(f"{d['kind']} {d['metadata']['name']}: frontend routing must not live in this chart")


def check_refusals(failures: list[str]) -> None:
    cases: list[tuple[str, list[str], str]] = [
        ("without a database source", runtime("database"), "requires database.url or database.existingSecret"),
        ("without kafka.brokers", runtime("kafka"), "kafka.brokers"),
        ("the log publisher", BASE + ["--set", "config.eventPublisher=log"], "the only publisher is"),
        ("an unknown publisher", BASE + ["--set", "config.eventPublisher=nats"], "the only publisher is"),
        ("EVENT_PUBLISHER=log through extraEnv",
         BASE + ["--set", "extraEnv[0].name=EVENT_PUBLISHER", "--set", "extraEnv[0].value=log"], "extraEnv sets EVENT_PUBLISHER=log"),
        ("a *_MODE through extraEnv",
         BASE + ["--set", "extraEnv[0].name=SOURCING_MODE", "--set", "extraEnv[0].value=kafka"], "extraEnv sets SOURCING_MODE"),
        ("a grace period equal to the shutdown sum", BASE + ["--set", "terminationGracePeriodSeconds=35"], "must exceed 35"),
        ("a grace period under the shutdown sum", BASE + ["--set", "terminationGracePeriodSeconds=20"], "must exceed 35"),
        ("a longer drain delay than the grace period covers",
         BASE + ["--set", "config.shutdownDrainDelay=30s", "--set", "terminationGracePeriodSeconds=60"], "must exceed 60"),
    ]
    for key in GROUPS:
        cases.append((f"without config.{key}", runtime(key), f"config.{key} is required"))
    for label, args, needle in cases:
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    everything = render(ENABLE_EVERYTHING)
    expected = [RELEASE, f"{RELEASE}-mcp", f"{RELEASE}-frontend"]
    check_components(everything, failures, expected)
    deployments = {d["metadata"]["name"]: d for d in everything if d.get("kind") == "Deployment"}
    services = {d["metadata"]["name"]: d for d in everything if d.get("kind") == "Service"}
    check_mcp(deployments, services, failures)
    check_frontend(everything, deployments, services, failures)

    # Default values must not deploy an HPA or a route, and must not render the
    # optional env that lets the binary's own defaults apply.
    defaults = render(BASE)
    stray = [d["kind"] for d in defaults if d.get("kind") in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute"}]
    if stray:
        failures.append(f"optional objects rendered with default values: {stray}")
    optional = [n for n in (d["metadata"]["name"] for d in defaults) if n.endswith(("-mcp", "-frontend"))]
    if optional:
        failures.append(f"mcp/frontend rendered with default values: {optional}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    off = {"OUTBOX_RELAY_INTERVAL", "SWEEP_INTERVAL"}
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")
    for want in ("KAFKA_BROKERS", "DATABASE_URL", *(k for k in API_ENV if k.endswith("_CONSUMER_GROUP"))):
        if want not in names:
            failures.append(f"{want} is mandatory and must always be rendered")

    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, frontend); mcp and frontend are off by "
          "default and render when enabled; every env var cmd/api and cmd/mcp read is rendered, the mcp Deployment "
          "gets no Kafka env and the frontend none at all; the chart refuses to render without a database source, "
          "brokers or any of the nine consumer groups, with a non-kafka publisher (also through extraEnv), with a "
          "*_MODE, or with a grace period that does not exceed the shutdown sum")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
