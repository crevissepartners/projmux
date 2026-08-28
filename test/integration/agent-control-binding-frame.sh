#!/usr/bin/env bash
set -euo pipefail

# Pin the real tmux display-message frame used by public Agent control lookup.
# This harness owns one run-unique socket below one run-unique TMUX_TMPDIR and
# never follows the invoking client's inherited route.
unset TMUX TMUX_PANE

test_root="$(mktemp -d "${TMPDIR:-/tmp}/projmux-control-binding.XXXXXX")"
tmux_root="$test_root/tmux"
socket_name="control-binding-$$-${RANDOM}"
session_name="control-binding-$$-${RANDOM}"
socket_path=""
mkdir -p "$tmux_root"

cleanup() {
  local current=""
  if [[ -n "$socket_path" ]]; then
    case "$socket_path" in
      "$tmux_root"/*) ;;
      *)
        echo "refusing control-binding cleanup outside isolated tmux root: $socket_path" >&2
        return 1
        ;;
    esac
    current="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$tmux_root" tmux -S "$socket_path" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -n "$current" ]]; then
      if [[ "$current" != "$socket_path" ]]; then
        echo "refusing control-binding cleanup after socket identity changed: expected=$socket_path current=$current" >&2
        return 1
      fi
      env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$tmux_root" tmux -S "$socket_path" kill-server
    fi
  fi
  rm -rf -- "$test_root"
}
trap cleanup EXIT

tmux_by_name() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$tmux_root" tmux -L "$socket_name" "$@"
}

tmux_by_name new-session -d -s "$session_name" sleep 300
# Resolve the physical socket immediately after creation. Every later read,
# write, and cleanup is bound to this validated exact path.
socket_path="$(tmux_by_name display-message -p -t "=$session_name" '#{socket_path}')"
case "$socket_path" in
  "$tmux_root"/*) ;;
  *)
    echo "control-binding socket escaped isolated tmux root: $socket_path" >&2
    exit 1
    ;;
esac

tmux_exact() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$tmux_root" tmux -S "$socket_path" "$@"
}

pane_id="$(tmux_exact list-panes -t "=$session_name" -F '#{pane_id}')"
if [[ -z "$pane_id" || "$pane_id" == *$'\n'* ]]; then
  echo "control-binding fixture did not resolve exactly one Pane" >&2
  exit 1
fi
pane_uid="pan-control-binding"
thread_id='thread\literal'
authority="provider-control-plane"
epoch="epoch-control-binding"
reason=' ready\literal '
tmux_exact set-option -p -t "$pane_id" @projmux_pane_uid "$pane_uid"
tmux_exact set-option -p -t "$pane_id" @projmux_ai_thread_id "$thread_id"
tmux_exact set-option -p -t "$pane_id" @projmux_codex_authority "$authority"
tmux_exact set-option -p -t "$pane_id" @projmux_codex_authority_epoch "$epoch"
tmux_exact set-option -p -t "$pane_id" @projmux_codex_authority_reason "$reason"

format='#{pane_id}\037#{@projmux_pane_uid}\037#{@projmux_ai_thread_id}\037#{@projmux_codex_authority}\037#{@projmux_codex_authority_epoch}\037#{@projmux_codex_authority_reason}'
actual="$(tmux_exact display-message -p -t "$pane_id" "$format")"
expected="${pane_id}\\037${pane_uid}\\037${thread_id}\\037${authority}\\037${epoch}\\037${reason}"
if [[ "$actual" != "$expected" ]]; then
  echo "real tmux control-binding frame changed" >&2
  printf 'actual=%q\nexpected=%q\n' "$actual" "$expected" >&2
  exit 1
fi

echo ">> real tmux Agent control-binding literal six-field frame passed"
