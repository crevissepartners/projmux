FROM golang:1.25-trixie

ARG FZF_VERSION=v0.65.2

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
    zsh \
  && rm -rf /var/lib/apt/lists/*

RUN go install "github.com/junegunn/fzf@${FZF_VERSION}"

WORKDIR /workspace
