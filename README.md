# Code sever operator
![Publish Docker images](https://github.com/opensourceways/code-server-operator/workflows/Publish%20Docker%20images/badge.svg?branch=stable)

This project used to launch multiple code server instances in k8s cluster.
there are three main code server types supported currently.
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

