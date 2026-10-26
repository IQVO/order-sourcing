{{/*
Expand the name of the chart.
*/}}
{{- define "order-sourcing.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "order-sourcing.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "order-sourcing.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "order-sourcing.labels" -}}
helm.sh/chart: {{ include "order-sourcing.chart" . }}
{{ include "order-sourcing.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. The only component is api; the Deployment and the Service
still pin app.kubernetes.io/component so a future second component cannot be
selected by accident (tests/test_service_selectors.py).
*/}}
{{- define "order-sourcing.selectorLabels" -}}
app.kubernetes.io/name: {{ include "order-sourcing.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "order-sourcing.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "order-sourcing.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "order-sourcing.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "order-sourcing.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "order-sourcing.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY: cmd/api exits at boot without DATABASE_URL; there is no in-memory
mode. Fails the render naming the values instead of crash-looping.
*/}}
{{- define "order-sourcing.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "order-sourcing requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY: cmd/api exits at boot without KAFKA_BROKERS and rejects
EVENT_PUBLISHER other than kafka. No default broker, no log sink.
*/}}
{{- define "order-sourcing.requireKafka" -}}
{{- $_ := required "order-sourcing requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it." .Values.kafka.brokers -}}
{{- if ne (toString .Values.config.eventPublisher) "kafka" -}}
{{- fail (printf "config.eventPublisher is %q — the only publisher is \"kafka\": the binary rejects any other EVENT_PUBLISHER at boot." (toString .Values.config.eventPublisher)) -}}
{{- end -}}
{{- end -}}

{{/*
All nine consumers always run, each under a STABLE consumer group id that comes
from values (cmd/api exits at boot without every <TOPIC>_CONSUMER_GROUP). A
stable id lets a rescheduled pod resume from its committed offsets; an id
built from the pod name would restart every consumer from scratch.
*/}}
{{- define "order-sourcing.requireConsumerGroups" -}}
{{- range $c := list (dict "key" "customerOrderingConsumerGroup" "env" "CUSTOMER_ORDERING_CONSUMER_GROUP") (dict "key" "deliveryPromiseConsumerGroup" "env" "DELIVERY_PROMISE_CONSUMER_GROUP") (dict "key" "lastMileDeliveryConsumerGroup" "env" "LAST_MILE_DELIVERY_CONSUMER_GROUP") (dict "key" "linehaulTransportationConsumerGroup" "env" "LINEHAUL_TRANSPORTATION_CONSUMER_GROUP") (dict "key" "nodePerformanceConsumerGroup" "env" "NODE_PERFORMANCE_CONSUMER_GROUP") (dict "key" "nodeTopologyConsumerGroup" "env" "NODE_TOPOLOGY_CONSUMER_GROUP") (dict "key" "offerAvailabilityConsumerGroup" "env" "OFFER_AVAILABILITY_CONSUMER_GROUP") (dict "key" "shippingAndTrackingConsumerGroup" "env" "SHIPPING_AND_TRACKING_CONSUMER_GROUP") (dict "key" "vendorConnectivityConsumerGroup" "env" "VENDOR_CONNECTIVITY_CONSUMER_GROUP") -}}
{{- $v := toString (index $.Values.config $c.key) -}}
{{- if or (eq $v "") (eq $v "<nil>") -}}
{{- fail (printf "config.%s is required (%s): the consumer always runs on real Kafka and cmd/api exits at boot without a group. Use a STABLE, service-prefixed id." $c.key $c.env) -}}
{{- end -}}
{{- if contains "$(" $v -}}
{{- fail (printf "config.%s is %q — a consumer group id must be a stable literal, never derived from the pod name or environment." $c.key $v) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
extraEnv may not bring back a non-kafka publisher, a *_MODE variable (the
binary has no modes), a *_CONSUMER_GROUP (those are config.*ConsumerGroup,
validated above) or an observability variable the chart renders itself.
*/}}
{{- define "order-sourcing.requireNoInMemoryEnv" -}}
{{- range .Values.extraEnv -}}
{{- $name := toString .name -}}
{{- $value := toString (default "" .value) -}}
{{- if and (eq $name "EVENT_PUBLISHER") (ne $value "kafka") -}}
{{- fail (printf "extraEnv sets EVENT_PUBLISHER=%s — the only publisher is \"kafka\"." $value) -}}
{{- end -}}
{{- if hasSuffix "_MODE" $name -}}
{{- fail (printf "extraEnv sets %s — *_MODE variables do not exist in order-sourcing: it runs on real Postgres and real Kafka only, with no permissive, log or off mode." $name) -}}
{{- end -}}
{{- if hasSuffix "_CONSUMER_GROUP" $name -}}
{{- fail (printf "extraEnv sets %s — set the consumer group through config.*ConsumerGroup (stable, never the pod name)." $name) -}}
{{- end -}}
{{- if has $name (list "ENVIRONMENT" "SERVICE_VERSION" "OTEL_SERVICE_NAME" "OTEL_EXPORTER_OTLP_ENDPOINT") -}}
{{- fail (printf "extraEnv sets %s, which the chart renders from its own values (environment, image.tag, otel.*): use those instead." $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The graceful shutdown of cmd/api is sequential: SHUTDOWN_DRAIN_DELAY + HTTP
drain 10s + consumers/sweep 10s + outbox relay 10s. terminationGracePeriodSeconds
must EXCEED drain delay + 30s or the kubelet SIGKILLs the pod between
"consumers stopped" and "relay stopped", stranding an uncommitted offset or an
in-flight outbox batch. The drain delay is whole seconds ("5s").
*/}}
{{- define "order-sourcing.requireGracePeriod" -}}
{{- $drain := toString .Values.config.shutdownDrainDelay -}}
{{- if not (regexMatch "^[0-9]+s$" $drain) -}}
{{- fail (printf "config.shutdownDrainDelay is %q — want whole seconds such as \"5s\" (or \"0s\" to disable): the terminationGracePeriodSeconds guard is computed from it." $drain) -}}
{{- end -}}
{{- $need := add (int (trimSuffix "s" $drain)) 30 -}}
{{- if le (int .Values.terminationGracePeriodSeconds) (int $need) -}}
{{- fail (printf "terminationGracePeriodSeconds is %v — it must exceed %v (config.shutdownDrainDelay %s + HTTP 10s + consumers 10s + outbox relay 10s): a shorter grace period SIGKILLs the consumers or the relay mid-drain." .Values.terminationGracePeriodSeconds $need $drain) -}}
{{- end -}}
{{- end -}}

{{/*
Every render-time requirement of the real runtime, in one include. The api
Deployment includes it, so a mis-set release fails `helm template` /
`helm install`.
*/}}
{{- define "order-sourcing.requireRealRuntime" -}}
{{- include "order-sourcing.requireDatabase" . -}}
{{- include "order-sourcing.requireKafka" . -}}
{{- include "order-sourcing.requireConsumerGroups" . -}}
{{- include "order-sourcing.requireNoInMemoryEnv" . -}}
{{- include "order-sourcing.requireGracePeriod" . -}}
{{- end -}}

{{/*
Fully qualified name of the MCP server Deployment/Service (cmd/mcp, ADR 0006).
*/}}
{{- define "order-sourcing.mcpFullname" -}}
{{- include "order-sourcing.fullname" . }}-mcp
{{- end }}
