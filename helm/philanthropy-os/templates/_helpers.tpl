{{- define "philos.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: {{ .partOf }}
{{- end -}}

{{- define "philos.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
{{- end -}}

{{- define "philos.protonmailBridge.imapAddr" -}}
{{- printf "protonmail-bridge.%s.svc.cluster.local:%v" .Release.Namespace .Values.protonmailBridge.ports.imap -}}
{{- end -}}

{{- define "philos.signalCli.httpUrl" -}}
{{- printf "http://signal-cli.%s.svc.cluster.local:%v" .Release.Namespace .Values.signalCli.ports.http -}}
{{- end -}}

{{- define "philos.mailIndexer.mcpUrl" -}}
{{- printf "http://mail-indexer.%s.svc.cluster.local:%v/mcp" .Release.Namespace .Values.mailIndexer.ports.mcp -}}
{{- end -}}

{{- define "philos.gdocsIndexer.mcpUrl" -}}
{{- printf "http://gdocs-indexer.%s.svc.cluster.local:%v/mcp" .Release.Namespace .Values.gdocsIndexer.ports.mcp -}}
{{- end -}}
