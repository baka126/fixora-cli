apiVersion: fixora.io/v1alpha1
kind: HPARepairPolicy
metadata:
  name: hpa-repair-acceptance
  namespace: FIXORA_NAMESPACE
spec:
  hpa:
    name: api-scale
  target:
    kind: Deployment
    name: api
  requests:
    - container: app
      cpu: 100m
