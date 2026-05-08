FROM node:24-trixie

ENV DEBIAN_FRONTEND=noninteractive
ENV GOPATH=/go
ENV GOMODCACHE=/go/pkg/mod
ENV GOTOOLCHAIN=local
ENV PATH=/go/bin:$PATH
ENV SHELL=/bin/bash
ENV TERM=xterm-256color

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    git \
    golang-go \
    make \
    ncurses-bin \
    procps \
    tmux \
  && rm -rf /var/lib/apt/lists/*

COPY go.mod /tmp/projmux-deps/
RUN cd /tmp/projmux-deps && go mod download

WORKDIR /work
