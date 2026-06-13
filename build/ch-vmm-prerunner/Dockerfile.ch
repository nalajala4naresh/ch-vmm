FROM golang:1.25-alpine AS builder

ARG GITHUB_TOKEN

RUN apk add --no-cache gcc musl-dev

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
RUN --mount=type=cache,target=/root/.cache/go-build go build -p `nproc` -a cmd/ch-vmm-prerunner/main.go

FROM alpine

RUN apk add --no-cache curl screen dnsmasq cdrkit iptables iproute2 qemu-virtiofsd dpkg util-linux s6-overlay nmap-ncat

RUN set -eux; \
    mkdir /var/lib/cloud-hypervisor; \
    case "$(uname -m)" in \
        'x86_64') \
            curl -sLo /usr/bin/cloud-hypervisor https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v50.0/cloud-hypervisor-static; \
            curl -sLo /usr/bin/ch-remote https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v50.0/ch-remote-static; \
            curl -sLo /var/lib/cloud-hypervisor/hypervisor-fw https://github.com/cloud-hypervisor/rust-hypervisor-firmware/releases/download/0.5.0/hypervisor-fw; \
            ;; \
        'aarch64') \
            curl -sLo /usr/bin/cloud-hypervisor https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v50.0/cloud-hypervisor-static-aarch64; \
            curl -sLo /usr/bin/ch-remote https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v50.0/ch-remote-static-aarch64; \
            curl -sLo /var/lib/cloud-hypervisor/CLOUDHV_EFI.fd https://github.com/nalajala4naresh/cloud-hypervisor-edk2-builder/releases/download/20220706/CLOUDHV_EFI.fd; \
            ;; \
        *) echo >&2 "error: unsupported architecture '$(uname -m)'"; exit 1 ;; \
    esac; \
    chmod +x /usr/bin/cloud-hypervisor; \
    chmod +x /usr/bin/ch-remote

COPY build/ch-vmm-prerunner/cloud-hypervisor-type /etc/s6-overlay/s6-rc.d/cloud-hypervisor/type
COPY build/ch-vmm-prerunner/cloud-hypervisor-run.sh /etc/s6-overlay/s6-rc.d/cloud-hypervisor/run
COPY build/ch-vmm-prerunner/cloud-hypervisor-finish.sh /etc/s6-overlay/s6-rc.d/cloud-hypervisor/finish
RUN touch /etc/s6-overlay/s6-rc.d/user/contents.d/cloud-hypervisor

COPY --from=builder /workspace/main /usr/bin/ch-vmm-prerunner
COPY build/ch-vmm-prerunner/ch-vmm-prerunner-type /etc/s6-overlay/s6-rc.d/ch-vmm-prerunner/type
COPY build/ch-vmm-prerunner/ch-vmm-prerunner-up /etc/s6-overlay/s6-rc.d/ch-vmm-prerunner/up
COPY build/ch-vmm-prerunner/ch-vmm-prerunner-run.sh /etc/s6-overlay/scripts/ch-vmm-prerunner-run.sh
RUN touch /etc/s6-overlay/s6-rc.d/user/contents.d/ch-vmm-prerunner
ENV S6_BEHAVIOUR_IF_STAGE2_FAILS=2

ENTRYPOINT ["/init"]

COPY build/ch-vmm-prerunner/iptables-wrapper /sbin/iptables-wrapper
RUN update-alternatives --install /sbin/iptables iptables /sbin/iptables-wrapper 100

ADD build/ch-vmm-prerunner/virt-init-volume.sh /usr/bin/virt-init-volume
