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

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:latest
WORKDIR /
COPY --from=builder /workspace/manager .

ENTRYPOINT ["/manager"]
