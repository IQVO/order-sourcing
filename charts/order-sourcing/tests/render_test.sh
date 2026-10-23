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
  { grep -ohE '(Getenv|getenv|env)\("[A-Z][A-Z0-9_]*"' "$repo_dir/cmd/api/main.go" | sed -E 's/.*\("//; s/"$//'
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
hpa="$(helm template "$release" "$chart_dir" "${base[@]}" --set autoscaling.api.enabled=true)"
grep -q 'kind: HorizontalPodAutoscaler' <<<"$hpa" || fail "the HPA did not render with autoscaling.api.enabled=true"
if grep -q '^  replicas:' <<<"$(helm template "$release" "$chart_dir" "${base[@]}" --set autoscaling.api.enabled=true --show-only templates/deployment.yaml)"; then
  fail "the Deployment must omit replicas when the HPA owns them"
fi

echo "PASS: order-sourcing chart render assertions"
