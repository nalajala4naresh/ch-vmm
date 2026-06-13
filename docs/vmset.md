# VMSet

VMSet is an Object that keeps disks around, beyond  the lifecycle of VM in contrary to VMPool

## StatefulSet Model

Similar to K8s Statefulset object, VMSet object will create bunch of identical VM's with index based name prefix


## DataVolumes
A non-empty `spec.dataVolumeTemplates` in a VMSet spec will indicate the desire to create DataVolumes during scaleup opration.
A scaleDown operation does keep the DataVolumes around & reuse the disks between scaleup and scaledown operations


```yaml 
apiVersion: cloudhypervisor.quill.today/v1beta1
kind: VMSet
metadata:
  name: ubuntu-ch-noble-set
  namespace: tenant1
spec:
  selector:
    matchLabels:
      requestor: team-A
  replicas: 1
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

