FROM docker.io/ubuntu:26.04
# https://github.com/nkrul/todo
# DEBUGGING: docker build -f .devcontainer/ubuntu.Dockerfile -t ubuntu-dev . && docker run -it ubuntu-dev bash
ARG USERNAME=ubuntu
ARG NAME=emissary
# TARGETARCH is provided automatically by BuildKit (amd64, arm64, ...)
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

# Install Go
ARG GO_SRC_FILE=go1.27.1.linux-${TARGETARCH}.tar.gz
RUN \
    curl -OL https://go.dev/dl/${GO_SRC_FILE} && \
    tar -C /usr/local -xf ${GO_SRC_FILE}
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

# Install Node.js via nvm (LTS)
RUN curl -o- https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.3/install.sh | bash
RUN /bin/bash -c ". .nvm/nvm.sh && nvm install --lts"

# Golang github 'insteadof' thingy
RUN \
    echo "[url \"ssh://git@github.com/\"]" >> .gitconfig && \
    echo "        insteadOf = https://github.com/" >> .gitconfig

# opencode
RUN curl -fsSL https://opencode.ai/install | bash