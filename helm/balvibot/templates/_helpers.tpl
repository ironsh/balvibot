{{/*
Render a fully-qualified image ref for a locally-built image, prefixing the
configurable .Values.imageRegistry when set. Call with a dict carrying the
registry and the image block, e.g.
  {{ include "balvibot.image" (dict "registry" $.Values.imageRegistry "image" .Values.api.image) }}
With imageRegistry empty (the committed default) this renders the bare
`repo:tag` and the kubelet resolves it from the node's local containerd store,
preserving the pre-registry behaviour. Set imageRegistry (e.g. to localhost:5000
in values.local.yaml) to pull these images from the on-node registry instead.
External/public images (postgres, busybox, iron-proxy) deliberately do not use
this helper, so they keep pulling from their upstream registries.
*/}}
{{- define "balvibot.image" -}}
{{- with .registry -}}{{ . }}/{{ end -}}{{ .image.repository }}:{{ .image.tag }}
{{- end -}}

{{/*
imagePullPolicy for a locally-built image: the per-image override (image.pullPolicy)
when set, otherwise the chart-wide .Values.imagePullPolicy default. Call as:
  {{ include "balvibot.imagePullPolicy" (dict "image" .Values.api.image "default" $.Values.imagePullPolicy) }}
Defaults to Always because locally-built images are pushed under fixed tags
(e.g. api:0.1.0); IfNotPresent would leave the node serving a stale image of the
same tag after a new push. Set imageRegistry's companion imagePullPolicy (or a
per-image pullPolicy) in values.local.yaml to change it.
*/}}
{{- define "balvibot.imagePullPolicy" -}}
{{- .image.pullPolicy | default .default -}}
{{- end -}}

{{- define "balvibot.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: {{ .partOf }}
{{- end -}}

{{- define "balvibot.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
{{- end -}}

{{- define "balvibot.protonmailBridge.imapAddr" -}}
{{- printf "protonmail-bridge.%s.svc.cluster.local:%v" .Release.Namespace .Values.protonmailBridge.ports.imap -}}
{{- end -}}

{{- define "balvibot.signalCli.httpUrl" -}}
{{- printf "http://signal-cli.%s.svc.cluster.local:%v" .Release.Namespace .Values.signalCli.ports.http -}}
{{- end -}}

{{- define "balvibot.api.mcpUrl" -}}
{{- printf "http://api.%s.svc.cluster.local:%v/mcp" .Release.Namespace .Values.api.ports.mcp -}}
{{- end -}}
