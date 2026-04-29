#!/usr/bin/env bash
echo "session=${PROJMUX_SESSION}"
echo "cwd=${PROJMUX_CWD}"
echo "kind=${PROJMUX_SESSION_KIND}"
echo "socket=${PROJMUX_SOCKET-unset}"
echo "version=${PROJMUX_VERSION}"
echo "stderr-line" >&2
exit 0
