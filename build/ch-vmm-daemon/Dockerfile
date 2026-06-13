FROM golang:1.25-alpine AS builder

ARG GITHUB_TOKEN

WORKDIR /workspace

# Install git for downloading private modules
RUN apk update && apk add --no-cache git

# Configure git to use token for private repos
RUN if [ -n "$GITHUB_TOKEN" ]; then \
        git config --global url."https://${GITHUB_TOKEN}:${GITHUB_TOKEN}@github.com/".insteadOf "https://github.com/"; \
    fi

# Disable git interactive prompts
RUN git config --global credential.helper store && \
    git config --global user.email "action@github.com" && \
    git config --global user.name "GitHub Action"

# Set GOPRIVATE to avoid proxy issues with private modules
ENV GOPRIVATE=github.com/nalajala4naresh/*

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download


COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -p `nproc`  -a cmd/ch-vmm-daemon/main.go



FROM alpine
COPY --from=builder /workspace/main /usr/bin/ch-vmm-daemon
ENTRYPOINT ["ch-vmm-daemon"]
