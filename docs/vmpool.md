# VMPool 

More often you would want to run more than a single VM at a time.

## Deployment Model

Similar to K8s Deployment object, VMPool object will create bunch of identical VM's with random naming convension for VM'S 


## DataVolumes
A non-empty `spec.dataVolumeTemplates` in a VMPool spec will indicate the desire to create DataVolumes tied to the lifecycle of VM's.
A scaleDown operation does not preserve the DataVolume objects.  

Example:

```yaml
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMPool
metadata:
  name: ubuntu-ch-noble-pool
  namespace: tenant1
spec:
  selector:
    matchLabels:
      requestor: team-A
  replicas: 3
  virtualMachineTemplate:
    metadata:
      labels:
        requestor: team-A
    spec:
      instance:
       memory:
        size: 1Gi
       disks:
        - name: jammy
        - name: cloud-init
       interfaces:
        - name: pod
      volumes:
      - name: jammy
        dataVolume:
          volumeName: jammy

      - name: cloud-init
        cloudInit:
          userData: |-
           #cloud-config
           password: password
           chpasswd: { expire: False }
           ssh_pwauth: True
      networks:
      - name: pod
        pod: {}
  dataVolumeTemplates:
    - metadata:
       name: jammy
       annotations:
         volume.kubernetes.io/selected-node: "ch-vmm"
      spec:
        storage:
         resources:
          requests:
           storage: 5Gi
         volumeMode: Filesystem
         storageClassName: local-path-immediate 
         accessModes:
         - ReadWriteOnce
        source:
         http:
          url: "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img"