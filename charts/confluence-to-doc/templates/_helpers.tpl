{{/* Expand the name of the chart. */}}
{{- define "confluence-to-doc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name. */}}
{{- define "confluence-to-doc.fullname" -}}
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

{{- define "confluence-to-doc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "confluence-to-doc.labels" -}}
helm.sh/chart: {{ include "confluence-to-doc.chart" . }}
{{ include "confluence-to-doc.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "confluence-to-doc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "confluence-to-doc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "confluence-to-doc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "confluence-to-doc.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "confluence-to-doc.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}

{{/*
Name of the Secret holding the values this chart manages itself. Values taken
from a Secret the operator already owns are referenced directly instead.
*/}}
{{- define "confluence-to-doc.secretName" -}}
{{- printf "%s-env" (include "confluence-to-doc.fullname" .) }}
{{- end }}

{{- define "confluence-to-doc.configMapName" -}}
{{- printf "%s-env" (include "confluence-to-doc.fullname" .) }}
{{- end }}

{{- define "confluence-to-doc.pvcName" -}}
{{- default (printf "%s-exports" (include "confluence-to-doc.fullname" .)) .Values.storage.filesystem.existingClaim }}
{{- end }}

{{/* Fail early on the settings the application cannot start without. */}}
{{- define "confluence-to-doc.validate" -}}
{{- if not .Values.publicUrl }}{{ fail "publicUrl is required: it sets the OIDC redirect URI and the same-origin check" }}{{ end }}
{{- if not .Values.confluence.baseUrl }}{{ fail "confluence.baseUrl is required" }}{{ end }}
{{- if not .Values.oidc.issuerUrl }}{{ fail "oidc.issuerUrl is required" }}{{ end }}
{{- if not .Values.oidc.clientId }}{{ fail "oidc.clientId is required" }}{{ end }}
{{- if and (not .Values.oidc.clientSecret) (not .Values.oidc.existingSecret) }}{{ fail "set oidc.clientSecret or oidc.existingSecret" }}{{ end }}
{{- if and (not .Values.database.url) (not .Values.database.existingSecret) }}{{ fail "set database.url or database.existingSecret: this chart deploys no database" }}{{ end }}
{{- if and (not .Values.encryptionKey) (not .Values.existingEncryptionKeySecret) }}{{ fail "set encryptionKey or existingEncryptionKeySecret (openssl rand -base64 32)" }}{{ end }}
{{- if eq .Values.storage.type "s3" }}
{{- if not .Values.storage.s3.bucket }}{{ fail "storage.s3.bucket is required when storage.type is s3" }}{{ end }}
{{- else if eq .Values.storage.type "filesystem" }}
{{- if and .Values.worker.enabled (not (has "ReadWriteMany" .Values.storage.filesystem.accessModes)) }}
{{- fail "filesystem storage shared by API and worker pods needs ReadWriteMany; use storage.type=s3, or set worker.enabled=false" }}
{{- end }}
{{- else }}{{ fail (printf "storage.type must be s3 or filesystem, got %q" .Values.storage.type) }}{{ end }}
{{- if .Values.wordTemplate.enabled }}
{{- if and (not .Values.wordTemplate.existingConfigMap) (not .Values.wordTemplate.existingSecret) }}
{{- fail "wordTemplate.enabled needs wordTemplate.existingConfigMap or wordTemplate.existingSecret" }}
{{- end }}
{{- end }}
{{- if .Values.telemetry.enabled }}
{{- if not .Values.telemetry.endpoint }}{{ fail "telemetry.enabled needs telemetry.endpoint" }}{{ end }}
{{- end }}
{{- end }}

{{/* Render a map as the comma-separated list OTEL_RESOURCE_ATTRIBUTES expects. */}}
{{- define "confluence-to-doc.resourceAttributes" -}}
{{- $pairs := list -}}
{{- range $k, $v := . -}}
{{- $pairs = append $pairs (printf "%s=%v" $k $v) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end }}

{{/* True when this chart owns a Secret of its own (see secret.yaml). */}}
{{- define "confluence-to-doc.hasManagedSecret" -}}
{{- $managed := false -}}
{{- if and .Values.database.url (not .Values.database.existingSecret) }}{{ $managed = true }}{{ end -}}
{{- if and .Values.oidc.clientSecret (not .Values.oidc.existingSecret) }}{{ $managed = true }}{{ end -}}
{{- if and .Values.encryptionKey (not .Values.existingEncryptionKeySecret) }}{{ $managed = true }}{{ end -}}
{{- if and (eq .Values.storage.type "s3") (not .Values.storage.s3.existingSecret) .Values.storage.s3.accessKeyId }}{{ $managed = true }}{{ end -}}
{{- if $managed }}true{{ end -}}
{{- end }}

{{/*
Pod template shared by the API and the worker: they run the same image with a
different APP_ROLE. Call with (dict "ctx" $ "component" "api" "role" "api").
*/}}
{{- define "confluence-to-doc.podTemplate" -}}
{{- $ctx := .ctx -}}
{{- $values := index $ctx.Values .component -}}
metadata:
  labels:
    {{- include "confluence-to-doc.selectorLabels" $ctx | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
    {{- with $values.podLabels }}{{- toYaml . | nindent 4 }}{{- end }}
  annotations:
    # Roll the pods when the configuration changes.
    checksum/config: {{ include (print $ctx.Template.BasePath "/configmap.yaml") $ctx | sha256sum }}
    checksum/secret: {{ include (print $ctx.Template.BasePath "/secret.yaml") $ctx | sha256sum }}
    {{- with $values.podAnnotations }}{{- toYaml . | nindent 4 }}{{- end }}
spec:
  {{- with $ctx.Values.imagePullSecrets }}
  imagePullSecrets: {{- toYaml . | nindent 4 }}
  {{- end }}
  serviceAccountName: {{ include "confluence-to-doc.serviceAccountName" $ctx }}
  automountServiceAccountToken: {{ $ctx.Values.serviceAccount.automountServiceAccountToken }}
  securityContext: {{- toYaml $ctx.Values.podSecurityContext | nindent 4 }}
  {{- with $values.terminationGracePeriodSeconds }}
  terminationGracePeriodSeconds: {{ . }}
  {{- end }}
  containers:
    - name: {{ $ctx.Chart.Name }}
      image: {{ include "confluence-to-doc.image" $ctx | quote }}
      imagePullPolicy: {{ $ctx.Values.image.pullPolicy }}
      securityContext: {{- toYaml $ctx.Values.securityContext | nindent 8 }}
      env:
        - name: APP_ROLE
          value: {{ .role | quote }}
        {{- if $ctx.Values.database.existingSecret }}
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: {{ $ctx.Values.database.existingSecret }}
              key: {{ $ctx.Values.database.existingSecretKey }}
        {{- end }}
        {{- if $ctx.Values.oidc.existingSecret }}
        - name: OIDC_CLIENT_SECRET
          valueFrom:
            secretKeyRef:
              name: {{ $ctx.Values.oidc.existingSecret }}
              key: {{ $ctx.Values.oidc.existingSecretKey }}
        {{- end }}
        {{- if $ctx.Values.existingEncryptionKeySecret }}
        - name: ENCRYPTION_KEY
          valueFrom:
            secretKeyRef:
              name: {{ $ctx.Values.existingEncryptionKeySecret }}
              key: {{ $ctx.Values.existingEncryptionKeySecretKey }}
        {{- end }}
        {{- if and (eq $ctx.Values.storage.type "s3") $ctx.Values.storage.s3.existingSecret }}
        - name: S3_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: {{ $ctx.Values.storage.s3.existingSecret }}
              key: {{ $ctx.Values.storage.s3.existingSecretAccessKeyIdKey }}
        - name: S3_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: {{ $ctx.Values.storage.s3.existingSecret }}
              key: {{ $ctx.Values.storage.s3.existingSecretSecretAccessKeyKey }}
        {{- end }}
        {{- with $ctx.Values.extraEnv }}{{- toYaml . | nindent 8 }}{{- end }}
      envFrom:
        - configMapRef:
            name: {{ include "confluence-to-doc.configMapName" $ctx }}
        {{- if include "confluence-to-doc.hasManagedSecret" $ctx }}
        - secretRef:
            name: {{ include "confluence-to-doc.secretName" $ctx }}
        {{- end }}
        {{- with $ctx.Values.extraEnvFrom }}{{- toYaml . | nindent 8 }}{{- end }}
      ports:
        - name: http
          containerPort: 8080
          protocol: TCP
      livenessProbe:
        httpGet:
          path: /healthz
          port: http
        initialDelaySeconds: 10
        periodSeconds: 20
      readinessProbe:
        httpGet:
          path: /readyz
          port: http
        initialDelaySeconds: 5
        periodSeconds: 10
      resources: {{- toYaml $values.resources | nindent 8 }}
      volumeMounts:
        # LibreOffice writes its profile and the document being converted here.
        - name: tmp
          mountPath: /tmp
        - name: home
          mountPath: /home/app
        {{- if eq $ctx.Values.storage.type "filesystem" }}
        - name: exports
          mountPath: {{ $ctx.Values.storage.filesystem.path }}
        {{- end }}
        {{- if $ctx.Values.wordTemplate.enabled }}
        - name: word-template
          mountPath: {{ $ctx.Values.wordTemplate.mountPath }}
          readOnly: true
        {{- end }}
  volumes:
    - name: tmp
      emptyDir:
        sizeLimit: {{ $ctx.Values.tmpVolume.sizeLimit }}
    - name: home
      emptyDir: {}
    {{- if eq $ctx.Values.storage.type "filesystem" }}
    - name: exports
      persistentVolumeClaim:
        claimName: {{ include "confluence-to-doc.pvcName" $ctx }}
    {{- end }}
    {{- if $ctx.Values.wordTemplate.enabled }}
    - name: word-template
      {{- if $ctx.Values.wordTemplate.existingConfigMap }}
      configMap:
        name: {{ $ctx.Values.wordTemplate.existingConfigMap }}
      {{- else }}
      secret:
        secretName: {{ $ctx.Values.wordTemplate.existingSecret }}
      {{- end }}
    {{- end }}
  {{- with $values.nodeSelector }}
  nodeSelector: {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $values.tolerations }}
  tolerations: {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with $values.affinity }}
  affinity: {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
