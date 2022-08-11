# Code sever operator
![Publish Docker images](https://github.com/opensourceways/code-server-operator/workflows/Publish%20Docker%20images/badge.svg?branch=stable)

This project used to launch multiple code server instances in k8s cluster.
there are three main code server types supported currently.

# Spec introduction
Definition of code server spec are describe below
```go
// CodeServerSpec defines the desired state of CodeServer
type CodeServerSpec struct {
	// Runtime specifies the different server runtime, 'lxd', 'code' and 'generic' are supported currently supported, for most of the container applications, generic is enough.
	Runtime RuntimeType `json:"runtime,omitempty" protobuf:"bytes,1,opt,name=runtime"`
	// Specifies the additional storage to be mounted in the location of 'WorkspaceLocation', use this with the combination of 'WorkspaceLocation' and 'StorageName'.
	StorageSize string `json:"storageSize,omitempty" protobuf:"bytes,2,opt,name=storageSize"`
	// Specifies the storage name for the workspace volume possible values are available sc name or 'emptyDir'
	StorageName string `json:"storageName,omitempty" protobuf:"bytes,3,opt,name=storageName"`
	// Specifies the additional annotations for persistent volume claim
	StorageAnnotations map[string]string `json:"storageAnnotations,omitempty" protobuf:"bytes,4,opt,name=storageAnnotations"`
	// Specifies workspace location.
	WorkspaceLocation string `json:"workspaceLocation,omitempty" protobuf:"bytes,5,opt,name=workspaceLocation"`
	// Specifies the resource requirements for code server pod.
	Resources v1.ResourceRequirements `json:"resources,omitempty" protobuf:"bytes,6,opt,name=resources"`
	// Specifies ingress bandwidth for code server
	IngressBandwidth string `json:"ingressBandwidth,omitempty" protobuf:"bytes,7,opt,name=ingressBandwidth"`
	// Specifies egress bandwidth for code server
	EgressBandwidth string `json:"egressBandwidth,omitempty" protobuf:"bytes,8,opt,name=egressBandwidth"`
	// Specifies the period before controller inactive the resource (delete all resources except volume).
	InactiveAfterSeconds *int64 `json:"inactiveAfterSeconds,omitempty" protobuf:"bytes,9,opt,name=inactiveAfterSeconds"`
	// Specifies the period before controller recycle the resource (delete all resources).
	RecycleAfterSeconds *int64 `json:"recycleAfterSeconds,omitempty" protobuf:"bytes,10,opt,name=recycleAfterSeconds"`
	// Specifies the subdomain for pod visiting
	Subdomain string `json:"subdomain,omitempty" protobuf:"bytes,11,opt,name=subdomain"`
	// Specifies the envs of container
	Envs []v1.EnvVar `json:"envs,omitempty" protobuf:"bytes,12,opt,name=envs"`
	// Specifies the command of container, only work in generic
	Command []string `json:"command,omitempty" protobuf:"bytes,13,rep,name=command"`
	// Specifies the Args of container, will be ignored if command specified, only work in generic
	Args []string `json:"args,omitempty" protobuf:"bytes,14,opt,name=args"`
	// Specifies the image used to running code server
	Image string `json:"image,omitempty" protobuf:"bytes,15,opt,name=image"`
	// Specifies the alive probe to detect whether pod is connected. Only http path are supported and time should be in
	// the format of 2006-01-02T15:04:05.000Z. if 'InactiveAfterSeconds' is none-zero, code server controller will use the
	// latest time collected from 'ConnectProbe' to decide whether should inactive the instance.
	ConnectProbe string `json:"connectProbe,omitempty" protobuf:"bytes,16,opt,name=connectProbe"`
	// Whether to enable pod privileged
	Privileged *bool `json:"privileged,omitempty" protobuf:"bytes,17,opt,name=privileged"`
	// Specifies the init plugins that will be running to finish before code server running. currently, only git plugin supported.
	InitPlugins map[string][]string `json:"initPlugins,omitempty" protobuf:"bytes,18,opt,name=initPlugins"`
	// Specifies the node selector for scheduling.
	NodeSelector map[string]string `json:"nodeSelector,omitempty" protobuf:"bytes,19,opt,name=nodeSelector"`
	// Specifies the liveness Probe.
	LivenessProbe *v1.Probe `json:"livenessProbe,omitempty" protobuf:"bytes,20,opt,name=livenessProbe"`
	// Specifies the readiness Probe.
	ReadinessProbe *v1.Probe `json:"readinessProbe,omitempty" protobuf:"bytes,19,opt,name=readinessProbe"`
	// Specifies the terminal container port for connection, defaults in 8080.
	ContainerPort string `json:"containerPort,omitempty" protobuf:"bytes,20,opt,name=containerPort"`
}
```
## Gotty based web terminal
This code server used to launch gotty based terminal in docker container, the example of CRD yaml is:
```shell
apiVersion: cs.opensourceways.com/v1alpha1
kind: CodeServer
metadata:
  name: codeserver-for-gotty-terminal
spec:
  runtime: generic
  subdomain: codeservertommy
  image: "opensourceway/openeuler-20.03-lts-sp1-base:latest"
  storageSize: "100Mi"
  storageName: "emptyDir"
  storageAnnotations:
    everest.io/disk-volume-type: SSD
  ingressBandwidth: "10M"
  egressBandwidth: "10M"
  inactiveAfterSeconds: 12000
  recycleAfterSeconds: 24000
  containerPort: "8080"
  workspaceLocation: "/workspace"
  envs:
    - name: GOTTY_ONLY_ENABLE_BACKEND_WS_SERVER
      value: "false"
    - name: GOTTY_CREDENTIAL
      value: name:password
    - name: SHELL_USER
      value: husheng
    - name: GOTTY_MAX_CONNECTION
      value: "10"
    - name: COMMUNITY_EMAIL
      value: contact@tommy.io
    - name: GOTTY_WS_ORIGIN
      value: ".*"
    - name: GOTTY_PERMIT_WRITE
      value: "true"
    - name: GOTTY_PORT
      value: "8080"
  args:
    - zsh
  connectProbe: "/active-time"
  privileged: false
  resources:
    requests:
      cpu: "1"

```
## Gotty based web terminal on LXD environment
This code server act as a LXD instance proxying which will direct lxd server on the same host to launch the specified
container or vm, start the gotty server and then proxying the internal network, the example of CRD yaml is:
```shell
apiVersion: cs.opensourceways.com/v1alpha1
kind: CodeServer
metadata:
  name: codeserver-lxd-sample
spec:
  runtime: lxd
  subdomain: codeservertommy
  image: "opensourceway/playground-lxc-launcher:sha-f6b536b"
  storageSize: "100Gi"
  storageName: "default"
  ingressBandwidth: "10M"
  egressBandwidth: "10M"
  inactiveAfterSeconds: 0
  recycleAfterSeconds: 12000
  workspaceLocation: "/workspace"
  envs:
    # env for gotty
    - name: GOTTY_ONLY_ENABLE_BACKEND_WS_SERVER
      value: "false"
    - name: GOTTY_CREDENTIAL
      value: name:password
    - name: COMMUNITY_USER
      value: zhangjianjun
    - name: GOTTY_MAX_CONNECTION
      value: "10"
    - name: COMMUNITY_EMAIL
      value: contact@openeuler.io
    - name: GOTTY_WS_ORIGIN
      value: ".*"
    - name: GOTTY_PERMIT_WRITE
      value: "true"
    - name: LAUNCHER_ADDITIONAL_CONFIG
      value: raw.lxc=lxc.apparmor.profile=unconfined,security.nesting=true,security.privileged=true
    - name: LAUNCHER_INSTANCE_PROFILES
      value: default
    - name: LAUNCHER_IMAGE_ALIAS
      value: openeuler-gotty
    - name: TERM
      value: xterm
  connectProbe: "/active-time"
  privileged: false
  resources:
    requests:
      cpu: "2"
      memory: "500Mi"
```
## VS Code terminal
This code server will launch a web based VS code instance, the example of CRD yaml is:
```shell
apiVersion: cs.opensourceways.com/v1alpha1
kind: CodeServer
metadata:
  name: codeserver-tommy
spec:
  runtime: lxd
  subdomain: codeservertommy
  image: "codercom/code-server:v2"
  volumeSize: "200m"
  storageClassName: "local-nfs"
  inactiveAfterSeconds: 600
  recycleAfterSeconds: 1200
  resources:
    requests:
      cpu: "2"
      memory: "2048m"
  initPlugins:
    git:
      - --repourl
      - https://github.com/TommyLike/tommylike.me.git
```

# PGWeb Instance
```shell
apiVersion: cs.opensourceways.com/v1alpha1
kind: CodeServer
metadata:
  name: pgweb-server
  namespace: default
spec:
  runtime: generic
  subdomain: pgweb-server-sample
  image: "opensourceway/opengauss-pgweb:0.0.5"
  inactiveAfterSeconds: 0
  recycleAfterSeconds: 1800
  command:
    - /bin/bash
    - -c
    - |
      whoami
      source ~/.bashrc
      /home/gauss/openGauss/install/bin/gs_ctl start -D /home/gauss/openGauss/data
      gsql -d postgres  -p 5432  -h 127.0.0.1  -U gauss -W openGauss2022 -c "CREATE USER opengauss with createdb IDENTIFIED BY 'openGauss2022'"
      /usr/bin/pgweb --bind=0.0.0.0 --listen=8080 --url "postgres://opengauss:openGauss2022@0.0.0.0:5432/postgres?sslmode=disable"
  connectProbe: "/"
  privileged: false
  containerPort: "8080"
  resources:
    requests:
      cpu: "1"
      memory: "1000Mi"
```

# Features
1. Release compute resource if the code server keeps inactive for some time.
2. Release volume resource if the code server has not been used for a period of long time.
3. Git clone code automatically before running code sever.
4. TLS/SSL enabled.
5. x86&arm supported.

# Develop
We use **kind** to boot up the kubernetes cluster, please use the script file to prepare cluster.
```$xslt
./local_development/local_up.sh
```
export `KUBECONFIG`:
```$xslt
export KUBECONFIG="$(kind get kubeconfig-path development)"
```
generate latest CRD yaml file:
```$xslt
make manifests
```
apply CRD into cluster:
```$xslt
make install
```
then test locally:
```$xslt
make run
```

