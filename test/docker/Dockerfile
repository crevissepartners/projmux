FROM golang:1.25-trixie

ENV DEBIAN_FRONTEND=noninteractive
ENV GOTOOLCHAIN=local
ENV PATH=/go/bin:$PATH

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    coreutils \
    git \
    make \
    ncurses-bin \
    procps \
    tmux \
    util-linux \
    zsh \
  && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
