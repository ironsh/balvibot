{{- define "philos.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: {{ .partOf }}
{{- end -}}

{{- define "philos.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
{{- end -}}
