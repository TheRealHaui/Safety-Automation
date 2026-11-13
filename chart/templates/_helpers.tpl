{{/*
Expand the name of the chart.
*/}}
{{- define "hoplon.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a fully qualified app name.
Truncated to 63 chars because Kubernetes name fields have that limit.
*/}}
{{- define "hoplon.fullname" -}}
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
Chart name and version label value.
*/}}
{{- define "hoplon.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "hoplon.labels" -}}
helm.sh/chart: {{ include "hoplon.chart" . }}
{{ include "hoplon.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels — used by Service and Deployment to match pods.
*/}}
{{- define "hoplon.selectorLabels" -}}
app.kubernetes.io/name: {{ include "hoplon.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "hoplon.serviceAccountName" -}}
{{- default (include "hoplon.fullname" .) .Values.serviceAccount.name }}
{{- end }}

{{/*
Name of the local-naming Secret. Used as the volume source when oracle.localNaming.enabled is true.
*/}}
{{- define "hoplon.localNamingSecretName" -}}
{{- include "hoplon.fullname" . }}-local-naming
{{- end }}
