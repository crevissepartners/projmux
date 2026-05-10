ARG BASE_IMAGE=golang:1.24-trixie
FROM ${BASE_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive
ENV GOPATH=/go
ENV GOMODCACHE=/go/pkg/mod
ENV GOTOOLCHAIN=local
ENV PATH=/usr/local/go/bin:/go/bin:$PATH
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8
ENV SHELL=/bin/bash
ENV TERM=xterm-256color

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    git \
    make \
    ncurses-bin \
    procps \
    tmux \
    util-linux \
  && rm -rf /var/lib/apt/lists/*

RUN ln -sf /usr/local/go/bin/go /usr/local/bin/go \
  && ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

COPY go.mod /tmp/projmux-deps/
RUN cd /tmp/projmux-deps && go mod download

WORKDIR /work
