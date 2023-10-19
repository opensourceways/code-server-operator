# Build the manager binary
FROM golang:1.20 as builder

ARG GO_ARCHITECTURE
ENV GO_ARCHITECTURE ${GO_ARCHITECTURE:-amd64}

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${DOCKER_ARCHITECTURE} GO111MODULE=on go build -a -buildmode=pie -ldflags "-s -linkmode 'external' -extldflags '-Wl,-z,now'" -o manager main.go

FROM openeuler/openeuler:22.03
ARG USERNAME=code-server
ARG USER_UID=1000
ARG USER_GID=$USER_UID

RUN yum install -y shadow && groupadd --gid $USER_GID $USERNAME \
    && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME

USER $USERNAME
WORKDIR /app
COPY --from=builder --chown=$USER_UID:$USER_GID /workspace/manager /app

ENTRYPOINT ["/app/manager"]
