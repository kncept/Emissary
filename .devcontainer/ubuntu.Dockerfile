FROM docker.io/ubuntu:26.04
# https://github.com/nkrul/todo
# DEBUGGING: docker build -f .devcontainer/ubuntu.Dockerfile -t ubuntu-dev . && docker run -it ubuntu-dev bash
ARG USERNAME=ubuntu
ARG NAME=emissary
# TARGETARCH is provided automatically by BuildKit (amd64, arm64, ...), but
# devcontainer builds may use the legacy builder where it is unset.
ARG TARGETARCH

# Locale stuff
RUN DEBIAN_FRONTEND=noninteractive apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y locales && \
    locale-gen en_US.UTF-8 && \
    rm -rf /var/lib/apt/lists/*
ENV LANG=en_US.UTF-8
ENV LANGUAGE=en_US:en
ENV LC_ALL=en_US.UTF-8

# Base tools
RUN apt-get update && \
    apt-get -y install sudo wget curl vim git unzip net-tools libatomic1 && \
    rm -rf /var/lib/apt/lists/*

# Install Go. Prefer an explicit GO_SRC_FILE override, otherwise derive the
# Go archive name from TARGETARCH (BuildKit) or dpkg (legacy builder) — both
# use the same amd64/arm64 names as the Go downloads.
ARG GO_VERSION=1.27.1
ARG GO_SRC_FILE
RUN set -eu; \
    if [ -z "${GO_SRC_FILE:-}" ]; then \
        arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
        case "$arch" in \
            x86_64) arch=amd64 ;; \
            aarch64) arch=arm64 ;; \
            amd64|arm64) ;; \
            *) echo "Unsupported Go architecture: $arch" >&2; exit 1 ;; \
        esac; \
        GO_SRC_FILE="go${GO_VERSION}.linux-${arch}.tar.gz"; \
    fi; \
    echo "Downloading ${GO_SRC_FILE}"; \
    curl -fsSLO "https://go.dev/dl/${GO_SRC_FILE}" && \
    tar -C /usr/local -xzf "${GO_SRC_FILE}" && \
    rm "${GO_SRC_FILE}" && \
    /usr/local/go/bin/go version
ENV PATH="${PATH}:/usr/local/go/bin"
ENV GOPRIVATE=*.kncept.com

# protoc
RUN DEBIAN_FRONTEND=noninteractive apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install --no-install-recommends --assume-yes protobuf-compiler && \
    rm -rf /var/lib/apt/lists/*

ENV GOPATH=/home/${USERNAME}/go
ENV PATH=$PATH:$GOPATH/bin
ENV GOROOT=/usr/local/go

# User
RUN echo $USERNAME ALL=\(root\) NOPASSWD:ALL > /etc/sudoers.d/$USERNAME \
    && chmod 0440 /etc/sudoers.d/$USERNAME
RUN echo "${USERNAME}:${USERNAME}" | chpasswd
USER ${USERNAME}
WORKDIR /home/${USERNAME}

# Install ogen (generates the Go API types from api/jmap.yaml and
# api/auth.yaml) and oapi-codegen
RUN go install github.com/ogen-go/ogen/cmd/ogen@latest && \
    go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

# Install Node.js via nvm (LTS)
RUN curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash
RUN /bin/bash -c ". .nvm/nvm.sh && nvm install --lts"

# Golang github 'insteadof' thingy
RUN \
    echo "[url \"ssh://git@github.com/\"]" >> .gitconfig && \
    echo "        insteadOf = https://github.com/" >> .gitconfig

# opencode
RUN curl -fsSL https://opencode.ai/install | bash