FROM archlinux:base-devel

RUN pacman -Syu --noconfirm \
    bash \
    bubblewrap \
    buildah \
    ca-certificates \
    curl \
    docker \
    docker-buildx \
    git \
    github-cli \
    gnupg \
    go \
    gzip \
    jq \
    make \
    nodejs \
    npm \
    openssh \
    protobuf \
    python \
    tar \
    unzip \
    xdg-utils \
    zstd \
    && pacman -Scc --noconfirm

ENV CGO_ENABLED=0
