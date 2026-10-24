{{/*
Fully qualified name of the frontend Module Federation remote deployment/service.

The remote (web/, `order_sourcing_mfe`) is served by its own nginx pod and
reached through the Nginx web gateway at /mfes/order-sourcing/. It is
deliberately a separate workload from the API: the Gateway API route never
points at it, and the api Service must never select it (component=frontend
vs component=api). Kept in its own helper file so the frontend component
stays self-contained (templates/frontend-*.yaml + this file + the frontend:
values block).
*/}}
{{- define "order-sourcing.frontendFullname" -}}
{{- include "order-sourcing.fullname" . }}-frontend
{{- end }}
