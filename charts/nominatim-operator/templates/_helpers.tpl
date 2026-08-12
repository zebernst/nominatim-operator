{{/*
Expand the name of the chart / release for resource naming hints.
*/}}
{{- define "nominatim-operator.name" -}}
{{- default .Chart.Name .Values.global.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Image tag: explicit tag, else Chart.AppVersion.
When image.digest is set (release publish), prefer digest and leave tag empty.
*/}}
{{- define "nominatim-operator.imageTag" -}}
{{- if .Values.image.digest -}}
{{- else if .Values.image.tag -}}
{{- .Values.image.tag -}}
{{- else -}}
{{- .Chart.AppVersion -}}
{{- end -}}
{{- end -}}

{{/*
Manager container args from leaderElection (and reserved watchNamespaces).
*/}}
{{- define "nominatim-operator.managerArgs" -}}
{{- $args := list -}}
{{- if .Values.leaderElection.enabled -}}
{{- $args = append $args "--leader-elect" -}}
{{- end -}}
{{- $args = append $args "--health-probe-bind-address=:8081" -}}
{{- toYaml $args -}}
{{- end -}}

{{/*
Build bjw-s common values from convenience keys + optional deep overrides.
*/}}
{{- define "nominatim-operator.commonValues" -}}
defaultPodOptions:
  automountServiceAccountToken: true
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  terminationGracePeriodSeconds: 10
controllers:
  manager:
    type: deployment
    replicas: {{ .Values.replicaCount | default 1 }}
    labels:
      control-plane: controller-manager
    annotations:
      kubectl.kubernetes.io/default-container: manager
    serviceAccount:
      identifier: manager
    containers:
      manager:
        nameOverride: manager
        command:
          - /manager
        args:
{{- include "nominatim-operator.managerArgs" . | nindent 10 }}
        image:
          repository: {{ .Values.image.repository | quote }}
          {{- if .Values.image.digest }}
          digest: {{ .Values.image.digest | quote }}
          {{- else }}
          tag: {{ include "nominatim-operator.imageTag" . | quote }}
          {{- end }}
          pullPolicy: {{ .Values.image.pullPolicy | quote }}
        resources:
{{- toYaml .Values.resources | nindent 10 }}
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
              - ALL
        probes:
          liveness:
            enabled: true
            custom: true
            spec:
              httpGet:
                path: /healthz
                port: 8081
              initialDelaySeconds: 15
              periodSeconds: 20
          readiness:
            enabled: true
            custom: true
            spec:
              httpGet:
                path: /readyz
                port: 8081
              initialDelaySeconds: 5
              periodSeconds: 10
serviceAccount:
  manager:
    enabled: true
    labels:
      control-plane: controller-manager
rbac:
  roles:
    manager:
      enabled: true
      type: ClusterRole
      labels:
        control-plane: controller-manager
      rules:
        - apiGroups:
            - nominatim.zebernst.dev
          resources:
            - nominatiminstances
            - nominatimoperations
          verbs:
            - create
            - delete
            - get
            - list
            - patch
            - update
            - watch
        - apiGroups:
            - nominatim.zebernst.dev
          resources:
            - nominatiminstances/finalizers
            - nominatimoperations/finalizers
          verbs:
            - update
        - apiGroups:
            - nominatim.zebernst.dev
          resources:
            - nominatiminstances/status
            - nominatimoperations/status
          verbs:
            - get
            - patch
            - update
    leader-election:
      enabled: {{ .Values.leaderElection.enabled }}
      type: Role
      labels:
        control-plane: controller-manager
      rules:
        - apiGroups:
            - ""
          resources:
            - configmaps
          verbs:
            - get
            - list
            - watch
            - create
            - update
            - patch
            - delete
        - apiGroups:
            - coordination.k8s.io
          resources:
            - leases
          verbs:
            - get
            - list
            - watch
            - create
            - update
            - patch
            - delete
        - apiGroups:
            - ""
          resources:
            - events
          verbs:
            - create
            - patch
  bindings:
    manager:
      enabled: true
      type: ClusterRoleBinding
      labels:
        control-plane: controller-manager
      roleRef:
        identifier: manager
      subjects:
        - identifier: manager
    leader-election:
      enabled: {{ .Values.leaderElection.enabled }}
      type: RoleBinding
      labels:
        control-plane: controller-manager
      roleRef:
        identifier: leader-election
      subjects:
        - identifier: manager
{{- end -}}
