{{- define "hatchet-sandbox.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "hatchet-sandbox.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "hatchet-sandbox.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "hatchet-sandbox.sandboxNamespace" -}}
{{- .Values.sandbox.namespace | default .Release.Namespace -}}
{{- end -}}

{{- define "hatchet-sandbox.credentialNames" -}}
{{- if .Values.sandbox.credentialNames -}}
{{- join "," .Values.sandbox.credentialNames -}}
{{- else -}}
{{- keys .Values.sandbox.credentials | sortAlpha | join "," -}}
{{- end -}}
{{- end -}}
