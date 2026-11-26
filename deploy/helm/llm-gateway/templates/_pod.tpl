{{/*
The gateway pod template, shared by the Deployment and the Argo Rollouts
Rollout (rollout.enabled). Include it under `template:` with nindent 4.
*/}}
{{- define "llm-gateway.podTemplate" -}}
metadata:
  {{- with .Values.podAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  labels:
    {{- include "llm-gateway.selectorLabels" . | nindent 4 }}
    {{- with .Values.podLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  serviceAccountName: {{ include "llm-gateway.serviceAccountName" . }}
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}
  {{- with .Values.imagePullSecrets }}
  imagePullSecrets:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: gateway
      image: {{ include "llm-gateway.image" . | quote }}
      imagePullPolicy: {{ .Values.image.pullPolicy }}
      args: ["serve", "-config", "/etc/llm-gateway/config.yaml"]
      ports:
        - name: http
          containerPort: 8080
      env:
        {{- if .Values.dev.redis.enabled }}
        - name: GATEWAY_REDIS_URL
          value: "redis://{{ include "llm-gateway.fullname" . }}-redis:6379/0"
        {{- end }}
        {{- if .Values.dev.postgres.enabled }}
        - name: GATEWAY_DB_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ include "llm-gateway.fullname" . }}-postgres
              key: password
        # $(VAR) is expanded by Kubernetes from the variable above.
        - name: GATEWAY_DATABASE_URL
          value: "postgres://gateway:$(GATEWAY_DB_PASSWORD)@{{ include "llm-gateway.fullname" . }}-postgres:5432/gateway?sslmode=disable"
        {{- end }}
        {{- with .Values.extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with (include "llm-gateway.secretName" .) }}
      envFrom:
        - secretRef:
            name: {{ . }}
      {{- end }}
      volumeMounts:
        - name: config
          mountPath: /etc/llm-gateway
          readOnly: true
      securityContext:
        {{- include "llm-gateway.containerSecurity" . | nindent 8 }}
      # startup: give the process up to 30 s to start; the other probes wait for it.
      startupProbe:
        httpGet: {path: /healthz, port: http}
        periodSeconds: 1
        failureThreshold: 30
      # liveness: restart the container if the process stops answering.
      livenessProbe:
        httpGet: {path: /healthz, port: http}
        periodSeconds: 10
        timeoutSeconds: 2
        failureThreshold: 3
      # readiness: take the pod out of the Service while it is not ready,
      # for example during the drain after SIGTERM.
      readinessProbe:
        httpGet: {path: /readyz, port: http}
        periodSeconds: 2
        timeoutSeconds: 2
        failureThreshold: 2
      resources:
        {{- toYaml .Values.resources | nindent 8 }}
  volumes:
    # A new config has a new ConfigMap name, and that rolls the pods.
    - name: config
      configMap:
        name: {{ include "llm-gateway.configName" . }}
  {{- if .Values.topologySpread.enabled }}
  topologySpreadConstraints:
    - maxSkew: {{ .Values.topologySpread.maxSkew }}
      topologyKey: kubernetes.io/hostname
      whenUnsatisfiable: {{ .Values.topologySpread.whenUnsatisfiable }}
      nodeTaintsPolicy: Honor   # do not count nodes the pod cannot run on
      # Spread the pods of each revision on their own. A Deployment labels
      # them pod-template-hash; an Argo Rollout, rollouts-pod-template-hash.
      matchLabelKeys: [{{ ternary "rollouts-pod-template-hash" "pod-template-hash" .Values.rollout.enabled }}]
      labelSelector:
        matchLabels:
          {{- include "llm-gateway.selectorLabels" . | nindent 10 }}
  {{- end }}
  {{- with .Values.nodeSelector }}
  nodeSelector:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.affinity }}
  affinity:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end -}}
