{{/*
  _helpers.tpl — Reusable template snippets used across all chart templates.

  These are Go template "functions" called with {{ include "name" . }}.
  They keep our templates DRY (Don't Repeat Yourself).
*/}}

{{/* Generate the full resource name, capped at 63 characters (Kubernetes limit) */}}
{{- define "cert-manager-webhook-easydns.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Standard labels applied to all resources */}}
{{- define "cert-manager-webhook-easydns.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels used to match pods to services */}}
{{- define "cert-manager-webhook-easydns.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
