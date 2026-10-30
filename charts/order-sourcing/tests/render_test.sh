#!/usr/bin/env bash
# Chart render assertions for order-sourcing. The chart renders exactly what
# cmd/api reads, nothing the binary ignores, and refuses to render the
# configurations that would crash-loop or lose messages: no database source, no
# Kafka brokers, an empty consumer group (nine of them), a *_MODE or a
# non-kafka publisher through extraEnv, a grace period that does not exceed the
# binary's sequential shutdown bounds.
# Run: bash charts/order-sourcing/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=order-sourcing
command -v helm >/dev/null || { echo "FAIL: helm is not installed" >&2; exit 1; }

# Dummy values, no password: nothing here is ever dialled.
groups=(
  customerOrderingConsumerGroup:order-sourcing-customer-ordering
  deliveryPromiseConsumerGroup:order-sourcing-delivery-promise
  lastMileDeliveryConsumerGroup:order-sourcing-last-mile-delivery
  linehaulTransportationConsumerGroup:order-sourcing-linehaul-transportation
  nodePerformanceConsumerGroup:order-sourcing-node-performance
  nodeTopologyConsumerGroup:order-sourcing-node-topology
  offerAvailabilityConsumerGroup:order-sourcing-offer-availability
  shippingAndTrackingConsumerGroup:order-sourcing-shipping-and-tracking
  vendorConnectivityConsumerGroup:order-sourcing-vendor-connectivity
)
base=(--set database.url=postgres://u@example.invalid:5432/db
  --set kafka.brokers=kafka.example.invalid:9092)
for g in "${groups[@]}"; do base+=(--set "config.${g%%:*}=${g#*:}"); done
# Every optional value set, so every env var the binary can read is rendered.
everything=(--set config.outboxRelayInterval=2s --set config.sweepInterval=30s --set config.shutdownDrainDelay=7s)

fail() { echo "FAIL: $*" >&2; exit 1; }

configmap="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/configmap.yaml)"
api_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" --show-only templates/deployment.yaml)"
all_deployment="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/deployment.yaml)"
all_configmap="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}" --show-only templates/configmap.yaml)"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# The api Deployment mounts the chart ConfigMap with envFrom, or a value that is
# only in the ConfigMap silently does nothing.
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q 'configMapRef:' || fail "api Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$api_deployment" | grep -q "name: $configmap_name" || fail "api Deployment envFrom does not reference ConfigMap $configmap_name"

# Chart vs binary: every env var cmd/api reads is rendered into the Deployment
# when every optional value is set. Names come from the binary's own
# Getenv/getenv/env literals plus the GroupEnv column of the consumer table.
env_vars() {
  { grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/api/main.go" "$repo_dir/cmd/api/telemetry.go" | sed -E 's/.*\("//; s/"$//'
    # ENVIRONMENT is read inside telemetry.Setup (documented in cmd/api/telemetry.go), not by a literal in cmd/api.
    echo ENVIRONMENT
    grep -ohE '"[A-Z][A-Z0-9_]*_CONSUMER_GROUP"' "$repo_dir/internal/adapters/inbound/kafka/handlers.go" | tr -d '"'
  } | sort -u
}
# shellcheck disable=SC2207
vars=($(env_vars))
(( ${#vars[@]} >= 19 )) || fail "found only ${#vars[@]} env names read by cmd/api; the scan is looking in the wrong place"
group_vars=0
for var in "${vars[@]}"; do
  grep -q "name: $var\$" <<<"$all_deployment" || fail "the api Deployment does not render $var, which cmd/api reads"
  [[ "$var" == *_CONSUMER_GROUP ]] && group_vars=$((group_vars + 1))
done
(( group_vars == 9 )) || fail "expected nine *_CONSUMER_GROUP variables read by the binary, found $group_vars"

# No dead keys: every ConfigMap key and every Deployment env name (except the
# DSN secrets) is a variable the binary reads, and none is set twice.
known=" ${vars[*]} "
while read -r key; do
  [[ "$known" == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$all_configmap")
dupes="$(grep -E '^            - name: ' <<<"$all_deployment" | sort | uniq -d || true)"
[[ -z "$dupes" ]] || fail "the Deployment renders env vars twice: $dupes"
while read -r key; do
  [[ "$known" == *" $key "* ]] || fail "Deployment env $key is read by no binary"
done < <(grep -E '^            - name: [A-Z]' <<<"$all_deployment" | sed -E 's/.*name: //')

env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
# Mandatory configuration is rendered from values, in the Deployment and the ConfigMap.
[[ "$(env_value "$api_deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
grep -q '^  KAFKA_BROKERS: "kafka.example.invalid:9092"' <<<"$configmap" || fail "the ConfigMap does not carry KAFKA_BROKERS"
[[ "$(env_value "$api_deployment" EVENT_PUBLISHER)" == "kafka" ]] || fail "EVENT_PUBLISHER must render as kafka"
for g in "${groups[@]}"; do
  key="${g%%:*}"; want="${g#*:}"
  var="$(sed -E 's/([a-z])([A-Z])/\1_\2/g' <<<"$key" | tr '[:lower:]' '[:upper:]')"
  [[ "$(env_value "$api_deployment" "$var")" == "$want" ]] || fail "the api Deployment does not render $var=$want"
  grep -q "^  $var: \"$want\"" <<<"$configmap" || fail "the ConfigMap does not carry $var=$want"
  case "$want" in order-sourcing-*) ;; *) fail "consumer group $want is not service-prefixed" ;; esac
done
[[ "$(env_value "$all_deployment" SHUTDOWN_DRAIN_DELAY)" == "7s" ]] || fail "config.shutdownDrainDelay is not rendered"
[[ "$(env_value "$all_deployment" SWEEP_INTERVAL)" == "30s" ]] || fail "config.sweepInterval is not rendered"
[[ "$(env_value "$all_deployment" OUTBOX_RELAY_INTERVAL)" == "2s" ]] || fail "config.outboxRelayInterval is not rendered"
grep -q '^  OUTBOX_RELAY_INTERVAL: "2s"' <<<"$all_configmap" || fail "the ConfigMap does not carry OUTBOX_RELAY_INTERVAL"
grep -q '^  SWEEP_INTERVAL: "30s"' <<<"$all_configmap" || fail "the ConfigMap does not carry SWEEP_INTERVAL"
# Optional values are absent while empty, so the binary's own defaults apply.
for var in OUTBOX_RELAY_INTERVAL SWEEP_INTERVAL; do
  if grep -q "name: $var\$" <<<"$api_deployment"; then fail "$var must not be rendered while its value is empty"; fi
done
[[ "$(env_value "$api_deployment" OUTBOX_MAX_ATTEMPTS)" == "20" ]] || fail "OUTBOX_MAX_ATTEMPTS must default to 20"
zero="$(helm template "$release" "$chart_dir" "${base[@]}" --set config.outboxMaxAttempts=0 --show-only templates/configmap.yaml)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$zero" || fail "config.outboxMaxAttempts=0 (unlimited) is not rendered"

# --- observability (fleet standard item 7, ADR 0008) ------------------------------------
# The api renders the fleet pipeline's env: the Collector endpoint, the service
# name, the version (image tag, appVersion when empty) and the environment.
otel_endpoint="otel-collector.observability.svc.cluster.local:4317"
[[ "$(env_value "$api_deployment" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] \
  || fail "the api Deployment does not render OTEL_EXPORTER_OTLP_ENDPOINT from otel.endpoint"
[[ "$(env_value "$api_deployment" OTEL_SERVICE_NAME)" == "order-sourcing" ]] \
  || fail "the api Deployment does not render OTEL_SERVICE_NAME=order-sourcing from otel.serviceName"
[[ "$(env_value "$api_deployment" SERVICE_VERSION)" == "0.1.0" ]] || fail "SERVICE_VERSION must default to the chart appVersion"
[[ "$(env_value "$api_deployment" ENVIRONMENT)" == "production" ]] || fail "ENVIRONMENT must come from environment"
tagged="$(helm template "$release" "$chart_dir" "${base[@]}" --set image.tag=v9.9.9 --set otel.serviceName=sourcing --set otel.endpoint=http://collector:4317 \
  --set environment=staging --show-only templates/deployment.yaml)"
[[ "$(env_value "$tagged" SERVICE_VERSION)" == "v9.9.9" ]] || fail "SERVICE_VERSION must follow image.tag"
[[ "$(env_value "$tagged" OTEL_SERVICE_NAME)" == "sourcing" ]] || fail "OTEL_SERVICE_NAME must follow otel.serviceName"
[[ "$(env_value "$tagged" OTEL_EXPORTER_OTLP_ENDPOINT)" == "http://collector:4317" ]] || fail "OTEL_EXPORTER_OTLP_ENDPOINT must follow otel.endpoint"
[[ "$(env_value "$tagged" ENVIRONMENT)" == "staging" ]] || fail "ENVIRONMENT must follow environment"
# otel.enabled=false renders no OTEL_* env (SERVICE_VERSION stays: it also labels the logs' build).
otel_off="$(helm template "$release" "$chart_dir" "${base[@]}" --set otel.enabled=false --set mcp.enabled=true --set a2a.enabled=true)"
if grep -q 'name: OTEL_' <<<"$otel_off"; then fail "otel.enabled=false must render no OTEL_* env in any component"; fi
grep -q 'name: SERVICE_VERSION$' <<<"$otel_off" || fail "SERVICE_VERSION must still render with otel.enabled=false"
# The values are chart-owned: extraEnv may not set them a second time.
for owned in OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT; do
  err="$(helm template "$release" "$chart_dir" "${base[@]}" --set "extraEnv[0].name=$owned" --set 'extraEnv[0].value=x' 2>&1)" \
    && fail "extraEnv must not be able to set $owned"
  grep -qF "extraEnv sets $owned" <<<"$err" || fail "the refusal for extraEnv $owned does not name it: $err"
done

# DATABASE_URL: Secret only; the migrations DSN is optional and falls back.
grep -A4 'name: DATABASE_URL$' <<<"$api_deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$api_deployment" | grep -q 'optional: true' || fail "MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi
existing="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db --set kafka.brokers=k:9092 \
  $(printf -- '--set config.%s ' "${groups[@]//:/=}") --show-only templates/deployment.yaml)"
grep -A4 'name: DATABASE_URL$' <<<"$existing" | grep -q 'name: my-db' || fail "database.existingSecret is not used for DATABASE_URL"
no_secret="$(helm template "$release" "$chart_dir" --set database.existingSecret=my-db --set kafka.brokers=k:9092 \
  $(printf -- '--set config.%s ' "${groups[@]//:/=}"))"
if grep -q 'kind: Secret' <<<"$no_secret"; then fail "the chart must not create a Secret when database.existingSecret is set"; fi
no_migrations="$(helm template "$release" "$chart_dir" "${base[@]}" --set database.migrationsExistingSecretKey= --show-only templates/deployment.yaml)"
if grep -q 'name: MIGRATIONS_DATABASE_URL' <<<"$no_migrations"; then fail "an empty migrationsExistingSecretKey must drop MIGRATIONS_DATABASE_URL"; fi

# No *_MODE variable, no log/permissive/off value, no in-memory wording.
render_all="$(helm template "$release" "$chart_dir" "${base[@]}" "${everything[@]}")"
if grep -qE 'name: [A-Z_]*_MODE$' <<<"$render_all"; then fail "the render carries a *_MODE variable; order-sourcing has no modes"; fi
if grep -qiE 'value: "?(permissive|log|off)"?$' <<<"$render_all"; then fail "the render carries permissive/log/off, which the binary rejects"; fi
if grep -qiE 'in-memory|permissive' <<<"$render_all"$'\n'"$(cat "$chart_dir/templates/NOTES.txt")"; then fail "the render or NOTES.txt mentions an in-memory / permissive mode"; fi

# Pod hardening, probes and shutdown room.
grep -q 'runAsNonRoot: true' <<<"$api_deployment" || fail "runAsNonRoot must be true"
grep -q 'readOnlyRootFilesystem: true' <<<"$api_deployment" || fail "readOnlyRootFilesystem must be true"
grep -q 'allowPrivilegeEscalation: false' <<<"$api_deployment" || fail "allowPrivilegeEscalation must be false"
grep -A2 'capabilities:' <<<"$api_deployment" | grep -q 'ALL' || fail "capabilities must drop ALL"
grep -A1 'seccompProfile:' <<<"$api_deployment" | grep -q 'RuntimeDefault' || fail "seccompProfile must be RuntimeDefault"
grep -A3 'livenessProbe:' <<<"$api_deployment" | grep -q 'path: /healthz' || fail "liveness must probe /healthz"
grep -A3 'readinessProbe:' <<<"$api_deployment" | grep -q 'path: /readyz' || fail "readiness must probe /readyz"
grep -q 'terminationGracePeriodSeconds: 60' <<<"$api_deployment" || fail "terminationGracePeriodSeconds should default to 60"

# --- refusals: the mandatory values name themselves -----------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':
$err"
}
mandatory_except() { # mandatory_except <database|kafka|<group key>>
  local skip="$1"
  [[ "$skip" == database ]] || printf '%s\n' --set database.url=postgres://u@h/db
  [[ "$skip" == kafka ]] || printf '%s\n' --set kafka.brokers=k:9092
  for g in "${groups[@]}"; do
    [[ "${g%%:*}" == "$skip" ]] || printf '%s\n' --set "config.${g%%:*}=${g#*:}"
  done
}
refusal_without() { # refusal_without <value to leave out> <needle>
  local args=()
  while IFS= read -r line; do args+=("$line"); done < <(mandatory_except "$1")
  expect_refusal "no $1" "$2" "${args[@]}"
}
refusal_without database "database.url or database.existingSecret"
refusal_without kafka "kafka.brokers"
for g in "${groups[@]}"; do
  refusal_without "${g%%:*}" "config.${g%%:*}"
  expect_refusal "an empty ${g%%:*}" "config.${g%%:*}" "${base[@]}" --set "config.${g%%:*}="
done
expect_refusal "a pod-name derived consumer group" 'config.customerOrderingConsumerGroup' "${base[@]}" --set 'config.customerOrderingConsumerGroup=os-$(POD_NAME)'
expect_refusal "the log publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=log
expect_refusal "an unknown publisher" "config.eventPublisher" "${base[@]}" --set config.eventPublisher=nats
expect_refusal "the log publisher through extraEnv" "EVENT_PUBLISHER" "${base[@]}" --set 'extraEnv[0].name=EVENT_PUBLISHER' --set 'extraEnv[0].value=log'
expect_refusal "a *_MODE through extraEnv" "_MODE" "${base[@]}" --set 'extraEnv[0].name=DEMAND_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "any *_MODE through extraEnv, even kafka" "_MODE" "${base[@]}" --set 'extraEnv[0].name=SOURCING_MODE' --set 'extraEnv[0].value=kafka'
expect_refusal "a consumer group through extraEnv" "config.*ConsumerGroup" "${base[@]}" --set 'extraEnv[0].name=NODE_TOPOLOGY_CONSUMER_GROUP' --set 'extraEnv[0].value=x'
helm template "$release" "$chart_dir" "${base[@]}" --set 'extraEnv[0].name=EVENT_PUBLISHER' --set 'extraEnv[0].value=kafka' >/dev/null \
  || fail "EVENT_PUBLISHER=kafka through extraEnv must still render"

# Grace period: must EXCEED drain delay + 30 (HTTP 10 + consumers 10 + relay 10).
expect_refusal "a grace period equal to the 35s sum" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=35
expect_refusal "a short grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=20
expect_refusal "no grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=0
helm template "$release" "$chart_dir" "${base[@]}" --set terminationGracePeriodSeconds=36 >/dev/null || fail "a 36s grace period (> 35s) must render"
expect_refusal "a grace period not covering a longer drain delay" "terminationGracePeriodSeconds" "${base[@]}" --set config.shutdownDrainDelay=15s --set terminationGracePeriodSeconds=45
helm template "$release" "$chart_dir" "${base[@]}" --set config.shutdownDrainDelay=15s --set terminationGracePeriodSeconds=46 >/dev/null || fail "46s must cover a 15s drain delay"
expect_refusal "a drain delay that is not whole seconds" "config.shutdownDrainDelay" "${base[@]}" --set config.shutdownDrainDelay=500ms

# Optional pieces are off by default.
for kind in HorizontalPodAutoscaler Ingress HTTPRoute; do
  if grep -q "kind: $kind" <<<"$(helm template "$release" "$chart_dir" "${base[@]}")"; then fail "$kind must not render by default"; fi
done
[[ "$(grep -c '^kind: Deployment' <<<"$render_all")" == 1 ]] || fail "exactly one Deployment (the api) must render by default"
if grep -qiE 'component: (mcp|a2a|frontend)' <<<"$render_all"; then fail "mcp, a2a and frontend must be off by default"; fi
hpa="$(helm template "$release" "$chart_dir" "${base[@]}" --set autoscaling.api.enabled=true)"
grep -q 'kind: HorizontalPodAutoscaler' <<<"$hpa" || fail "the HPA did not render with autoscaling.api.enabled=true"
if grep -q '^  replicas:' <<<"$(helm template "$release" "$chart_dir" "${base[@]}" --set autoscaling.api.enabled=true --show-only templates/deployment.yaml)"; then
  fail "the Deployment must omit replicas when the HPA owns them"
fi

# --- optional components (ADR 0006 mcp, ADR 0009 a2a, ADR 0007 console remote) ----------
# render_one <template> <extra helm args...>: one template, the mandatory base set.
render_one() { helm template "$release" "$chart_dir" --show-only "templates/$1" "${@:2}"; }
# binary_env_of <main.go>: the upper-case env literals a cmd reads.
binary_env_of() { grep -ohE '"[A-Z][A-Z0-9_]*"' "$1" | tr -d '"' | sort -u; }
assert_env() { # assert_env <rendered> <what> <VAR>...
  local rendered="$1" what="$2"; shift 2
  for var in "$@"; do grep -q "name: $var$" <<<"$rendered" || fail "$what does not render $var"; done
}

# mcp: its own env, never the api ConfigMap, never Kafka (ADR 0006).
mcp_dep="$(render_one mcp-deployment.yaml "${base[@]}" --set mcp.enabled=true)"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_dep" || fail "the mcp Deployment was not rendered with mcp.enabled=true"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_dep" || fail "the mcp Deployment must run /app/mcp"
if grep -q 'configMapRef:' <<<"$mcp_dep"; then fail "mcp-deployment.yaml must not mount the api ConfigMap"; fi
if grep -Eq 'name: (KAFKA_BROKERS|EVENT_PUBLISHER|OUTBOX_[A-Z_]+|SWEEP_[A-Z_]+|[A-Z]+_CONSUMER_GROUP)$' <<<"$mcp_dep"; then
  fail "the mcp Deployment must not get Kafka/relay/sweep/consumer env (read-only, never dials Kafka)"
fi
if grep -qE 'name: [A-Z_]*_MODE$' <<<"$mcp_dep"; then fail "no *_MODE variable may be rendered on the mcp Deployment"; fi
# shellcheck disable=SC2207
mcp_vars=($(binary_env_of "$repo_dir/cmd/mcp/main.go" "$repo_dir/cmd/mcp/telemetry.go"))
(( ${#mcp_vars[@]} >= 5 )) || fail "found only ${#mcp_vars[@]} env literals in cmd/mcp/main.go; the scan is looking in the wrong place"
assert_env "$mcp_dep" "the mcp Deployment" "${mcp_vars[@]}"
assert_env "$mcp_dep" "the mcp Deployment" OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT
[[ "$(env_value "$mcp_dep" SERVICE_VERSION)" == "0.1.0" ]] || fail "the mcp SERVICE_VERSION must default to the image tag / appVersion, like the api"
mcp_versioned="$(render_one mcp-deployment.yaml "${base[@]}" --set mcp.enabled=true --set mcp.serviceVersion=dev)"
[[ "$(env_value "$mcp_versioned" SERVICE_VERSION)" == "dev" ]] || fail "SERVICE_VERSION must come from mcp.serviceVersion"
[[ "$(env_value "$mcp_dep" OTEL_SERVICE_NAME)" == "order-sourcing-mcp" ]] || fail "the mcp component must export as <otel.serviceName>-mcp"
[[ "$(env_value "$mcp_dep" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] || fail "the mcp component must export to otel.endpoint"
mcp_named="$(render_one mcp-deployment.yaml "${base[@]}" --set mcp.enabled=true --set otel.serviceName=sourcing)"
[[ "$(env_value "$mcp_named" OTEL_SERVICE_NAME)" == "sourcing-mcp" ]] || fail "the mcp service name must follow otel.serviceName"
grep -A4 'name: DATABASE_URL$' <<<"$mcp_dep" | grep -q 'secretKeyRef:' || fail "the mcp Deployment's DATABASE_URL must come from a Secret"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$mcp_dep" | grep -q 'optional: true' || fail "the mcp Deployment's MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
dups="$(grep -E '^ {12}- name: ' <<<"$mcp_dep" | sort | uniq -d)"
[[ -z "$dups" ]] || fail "the mcp Deployment renders an env var twice: $dups"
mcp_svc="$(render_one mcp-service.yaml "${base[@]}" --set mcp.enabled=true)"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_svc" || fail "the mcp Service does not pin component=mcp"
grep -q 'type: ClusterIP' <<<"$mcp_svc" || fail "the mcp Service must stay ClusterIP"

# The host route rewrites /api/mcp/order-sourcing to the SDK's /mcp endpoint.
mcp_route="$(render_one mcp-route.yaml "${base[@]}" --set mcp.enabled=true --set mcp.route.enabled=true \
  --set mcp.route.parentRefs[0].name=gw --set mcp.route.parentRefs[0].namespace=kong)"
grep -q 'kind: HTTPRoute' <<<"$mcp_route" || fail "the mcp HTTPRoute was not rendered with mcp.route.enabled=true"
grep -q 'value: /api/mcp/order-sourcing' <<<"$mcp_route" || fail "the mcp route must carry the fleet host path"
grep -q 'replacePrefixMatch: /mcp' <<<"$mcp_route" || fail "the mcp route must rewrite the prefix to /mcp"
grep -q "name: ${release}-mcp" <<<"$mcp_route" || fail "the mcp route must target the mcp Service"

# a2a: the READ-ONLY A2A agent (ADR 0009): its own env, never the api ConfigMap,
# never Kafka, and off unless asked for.
a2a_dep="$(render_one a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true)"
grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_dep" || fail "the a2a Deployment was not rendered with a2a.enabled=true"
grep -q 'command: \["/app/a2a"\]' <<<"$a2a_dep" || fail "the a2a Deployment must run /app/a2a"
if grep -q 'configMapRef:' <<<"$a2a_dep"; then fail "a2a-deployment.yaml must not mount the api ConfigMap"; fi
if grep -qE 'component: (api|mcp|frontend)' <<<"$a2a_dep"; then fail "the a2a Deployment carries another component's label"; fi
if grep -Eq 'name: (KAFKA_BROKERS|EVENT_PUBLISHER|OUTBOX_[A-Z_]+|SWEEP_[A-Z_]+|[A-Z]+_CONSUMER_GROUP)$' <<<"$a2a_dep"; then
  fail "the a2a Deployment must not get Kafka/relay/sweep/consumer env (read-only, never dials Kafka)"
fi
if grep -qE 'name: [A-Z_]*_MODE$' <<<"$a2a_dep"; then fail "no *_MODE variable may be rendered on the a2a Deployment"; fi
# Every variable cmd/a2a names as REQUIRED is rendered, derived from the Go
# source so a new mandatory variable fails until the chart has it. (|| true: a
# scan that finds nothing must reach the fail message, not abort under set -e.)
a2a_vars="$(grep -rhoE --include='*.go' --exclude='*_test.go' '[A-Z][A-Z0-9_]{2,} is required' "$repo_dir/cmd/a2a" | sed -E 's/ is required//' | sort -u || true)"
[[ -n "$a2a_vars" ]] || fail "found no mandatory variables for cmd/a2a; the scan is looking in the wrong place"
for var in $a2a_vars; do
  grep -q "name: $var\$" <<<"$a2a_dep" || fail "the a2a Deployment does not render $var, which cmd/a2a requires"
done
# ...and every env literal cmd/a2a reads from its own composition root.
# shellcheck disable=SC2207
a2a_env=($(binary_env_of "$repo_dir/cmd/a2a/main.go"))
(( ${#a2a_env[@]} >= 5 )) || fail "found only ${#a2a_env[@]} env literals in cmd/a2a/main.go; the scan is looking in the wrong place"
assert_env "$a2a_dep" "the a2a Deployment" "${a2a_env[@]}"
assert_env "$a2a_dep" "the a2a Deployment" A2A_ADDR A2A_AGENT_URL OTEL_SERVICE_NAME OTEL_EXPORTER_OTLP_ENDPOINT SERVICE_VERSION ENVIRONMENT DATABASE_URL
[[ "$(env_value "$a2a_dep" A2A_ADDR)" == ":8093" ]] || fail "the a2a agent must listen on :8093 (8080 is the api, 8090 the mcp server)"
# The Agent Card must advertise a dialable absolute URL: it defaults to the
# release's in-cluster Service DNS (namespace-derived, so pattern-match) and
# stays overridable.
a2a_url="$(env_value "$a2a_dep" A2A_AGENT_URL || true)" # a missing var must reach the fail message below, not abort under set -e
[[ "$a2a_url" =~ ^http://[a-z0-9.-]+-a2a\.[a-z0-9-]+\.svc\.cluster\.local:8093/$ ]] || fail "A2A_AGENT_URL default is not the in-cluster Service DNS: $a2a_url"
a2a_override="$(render_one a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true --set a2a.agentUrl=http://gw.example/api/a2a/order-sourcing/)"
[[ "$(env_value "$a2a_override" A2A_AGENT_URL || true)" == "http://gw.example/api/a2a/order-sourcing/" ]] || fail "a2a.agentUrl does not override A2A_AGENT_URL"
[[ "$(env_value "$a2a_dep" SERVICE_VERSION)" == "0.1.0" ]] || fail "the a2a SERVICE_VERSION must default to the image tag / appVersion, like the api"
[[ "$(env_value "$a2a_dep" OTEL_SERVICE_NAME)" == "order-sourcing-a2a" ]] || fail "the a2a component must export as <otel.serviceName>-a2a"
[[ "$(env_value "$a2a_dep" OTEL_EXPORTER_OTLP_ENDPOINT)" == "$otel_endpoint" ]] || fail "the a2a component must export to otel.endpoint"
grep -A4 'name: DATABASE_URL$' <<<"$a2a_dep" | grep -q 'secretKeyRef:' || fail "the a2a Deployment's DATABASE_URL must come from a Secret"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$a2a_dep" | grep -q 'optional: true' || fail "the a2a Deployment's MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
dups="$(grep -E '^ {12}- name: ' <<<"$a2a_dep" | sort | uniq -d || true)"
[[ -z "$dups" ]] || fail "the a2a Deployment renders an env var twice: $dups"
a2a_extra="$(render_one a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true --set 'a2a.extraEnv[0].name=LOG_FORMAT' --set 'a2a.extraEnv[0].value=json')"
[[ "$(env_value "$a2a_extra" LOG_FORMAT || true)" == "json" ]] || fail "a2a.extraEnv is not rendered"
a2a_svc="$(render_one a2a-service.yaml "${base[@]}" --set a2a.enabled=true)"
grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_svc" || fail "the a2a Service does not pin component=a2a"
grep -q 'type: ClusterIP' <<<"$a2a_svc" || fail "the a2a Service must stay ClusterIP"
grep -A3 '^  selector:' <<<"$a2a_svc" | grep -q 'app.kubernetes.io/component: a2a' || fail "the a2a Service selector must pin component=a2a"
grep -q 'port: 8093' <<<"$a2a_svc" || fail "the a2a Service must expose 8093"
# Nothing a2a renders unless asked for (default values), route included.
for t in a2a-deployment.yaml a2a-service.yaml a2a-route.yaml; do
  if render_one "$t" "${base[@]}" 2>/dev/null | grep -q 'kind:'; then fail "templates/$t rendered with default values"; fi
done
if render_one a2a-route.yaml "${base[@]}" --set a2a.enabled=true 2>/dev/null | grep -q 'kind: HTTPRoute'; then fail "the a2a route must need a2a.route.enabled"; fi

# The host route rewrites /api/a2a/order-sourcing to the JSON-RPC root, so
# /.well-known/agent-card.json stays reachable under the same prefix.
a2a_route="$(render_one a2a-route.yaml "${base[@]}" --set a2a.enabled=true --set a2a.route.enabled=true \
  --set a2a.route.parentRefs[0].name=gw --set a2a.route.parentRefs[0].namespace=kong)"
grep -q 'kind: HTTPRoute' <<<"$a2a_route" || fail "the a2a HTTPRoute was not rendered with a2a.route.enabled=true"
grep -q 'value: /api/a2a/order-sourcing' <<<"$a2a_route" || fail "the a2a route must carry the fleet host path"
grep -q 'replacePrefixMatch: /$' <<<"$a2a_route" || fail "the a2a route must rewrite the prefix to /"
grep -q "name: ${release}-a2a" <<<"$a2a_route" || fail "the a2a route must target the a2a Service"
grep -q 'port: 8093' <<<"$a2a_route" || fail "the a2a route must target port 8093"
if grep -qiE 'konghq.com/plugins|auth' <<<"$a2a_route"; then fail "the a2a route must carry no plugin or auth (unauthenticated by fleet decision)"; fi

# frontend: the web remote (ADR 0007). Static bytes; no database, no Kafka.
frontend_dep="$(render_one frontend-deployment.yaml "${base[@]}" --set frontend.enabled=true)"
frontend_svc="$(render_one frontend-service.yaml "${base[@]}" --set frontend.enabled=true)"
grep -q 'app.kubernetes.io/component: frontend' <<<"$frontend_dep" || fail "the frontend Deployment was not rendered with frontend.enabled=true"
grep -q 'type: ClusterIP' <<<"$frontend_svc" || fail "the frontend Service must stay ClusterIP"
if grep -Eq 'name: (DATABASE_URL|MIGRATIONS_DATABASE_URL|KAFKA_BROKERS)$' <<<"$frontend_dep"; then fail "the frontend Deployment must not get database/Kafka env"; fi
if grep -q 'command:' <<<"$frontend_dep"; then fail "the frontend container runs the image's own nginx entrypoint"; fi

# All four components at once: every Service still selects exactly one
# Deployment (tests/test_service_selectors.py proves it structurally).
together="$(helm template "$release" "$chart_dir" "${base[@]}" --set mcp.enabled=true --set a2a.enabled=true --set frontend.enabled=true)"
[[ "$(grep -c '^kind: Deployment$' <<<"$together")" == 4 ]] || fail "api + mcp + a2a + frontend must render four Deployments"
[[ "$(grep -c '^kind: Service$' <<<"$together")" == 4 ]] || fail "api + mcp + a2a + frontend must render four Services"
if grep -E 'kind: (Ingress|HTTPRoute)' <<<"$together" | grep -q frontend; then
  fail "frontend routing must not live in this chart"
fi
# ...and the ci values file used by ct lint renders the same set.
ci_all="$(helm template "$release" "$chart_dir" -f "$chart_dir/ci/all-components-values.yaml")"
for needle in 'component: mcp' 'component: a2a' 'component: frontend' 'command: ["/app/mcp"]' 'command: ["/app/a2a"]'; do
  grep -qF "$needle" <<<"$ci_all" || fail "ci/all-components-values.yaml must render $needle"
done

echo "PASS: order-sourcing chart render assertions"
