#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_image="${PROJMUX_POC_NO_FZF_BASE_IMAGE:-golang:1.24-trixie}"
image="${PROJMUX_POC_NO_FZF_IMAGE:-projmux:poc-no-fzf-go124-trixie}"
dockerfile="$root/test/docker/no-fzf-poc.Dockerfile"

if [[ ! -t 0 || ! -t 1 || ! -t 2 ]]; then
  cat >&2 <<'EOF'
[poc/no-fzf] interactive sandbox requires a real TTY.

Do not run this through `wt run`; `wt run` captures stdio, so Docker cannot
attach an interactive terminal and the native picker will degrade into broken
escape-sequence input.

Run this one-liner from your normal terminal instead:

  bash "$(wt path poc/native-picker-no-fzf)/scripts/poc-native-picker-no-fzf-sandbox.sh"
EOF
  exit 2
fi

echo "[poc/no-fzf] building dependency image $image from $base_image"
docker build --pull=false --build-arg "BASE_IMAGE=$base_image" -f "$dockerfile" -t "$image" "$root"

echo "[poc/no-fzf] entering interactive no-fzf sandbox"
docker run --rm -it \
  --network none \
  -v "$root":/work \
  -w /work \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/tmp/projmux-gocache \
  -e GOTOOLCHAIN=local \
  -e LANG=C.UTF-8 \
  -e LC_ALL=C.UTF-8 \
  -e PROJMUX_PICKER_BACKEND=native \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  "$image" \
  bash -lc 'set -euo pipefail
    go build -o /usr/local/bin/projmux ./cmd/projmux
    if command -v fzf >/dev/null 2>&1; then
      echo "unexpected fzf on PATH" >&2
      exit 1
    fi
    demo_root=/workspace/projects
    mkdir -p "$demo_root/alpha-api" "$demo_root/bravo-web" "$demo_root/charlie-tools"
    for project in alpha-api bravo-web charlie-tools; do
      if [[ ! -d "$demo_root/$project/.git" ]]; then
        git -C "$demo_root/$project" init -q
      fi
      printf "# %s\n\nno-fzf projmux sandbox project\n" "$project" > "$demo_root/$project/README.md"
    done
    export PROJMUX_PROJDIR="$demo_root"
    export PROJMUX_MANAGED_ROOTS="$demo_root"
    sandbox_config=/tmp/projmux-native-tmux.conf
    popup_log=/tmp/projmux-popup.log
    popup_wrapper=/tmp/projmux-popup-toggle
    cat > "$popup_wrapper" <<'"'"'WRAPPER'"'"'
#!/usr/bin/env bash
set -u
log=/tmp/projmux-popup.log
client="${1:-}"
mode="${2:-}"
{
  printf "[%s] popup-toggle client=%q mode=%q backend=%q projdir=%q roots=%q\n" \
    "$(date -Is)" "$client" "$mode" "${PROJMUX_PICKER_BACKEND:-}" "${PROJMUX_PROJDIR:-}" "${PROJMUX_MANAGED_ROOTS:-}"
} >> "$log"
PROJMUX_PICKER_BACKEND="${PROJMUX_PICKER_BACKEND:-native}" \
PROJMUX_NATIVE_DEBUG_LOG="$log" \
PROJMUX_NATIVE_TTY_FALLBACK=0 \
PROJMUX_PROJDIR="${PROJMUX_PROJDIR:-/workspace/projects}" \
PROJMUX_MANAGED_ROOTS="${PROJMUX_MANAGED_ROOTS:-/workspace/projects}" \
  /usr/local/bin/projmux tmux popup-toggle --client "$client" "$mode" >> "$log" 2>&1
code=$?
printf "[%s] popup-toggle exit=%s client=%q mode=%q\n" "$(date -Is)" "$code" "$client" "$mode" >> "$log"
exit "$code"
WRAPPER
    chmod +x "$popup_wrapper"
    : > "$popup_log"
    projmux tmux print-app-config --bin /usr/local/bin/projmux > "$sandbox_config"
    {
      printf "set-environment -g PROJMUX_PICKER_BACKEND native\n"
      printf "set-environment -g PROJMUX_PROJDIR %q\n" "$PROJMUX_PROJDIR"
      printf "set-environment -g PROJMUX_MANAGED_ROOTS %q\n" "$PROJMUX_MANAGED_ROOTS"
      printf "set-environment -g SHELL /bin/bash\n"
    } >> "$sandbox_config"
    printf -v popup_env "env PROJMUX_PICKER_BACKEND=native PROJMUX_NATIVE_DEBUG_LOG=%q PROJMUX_NATIVE_TTY_FALLBACK=0 PROJMUX_PROJDIR=%q PROJMUX_MANAGED_ROOTS=%q" "$popup_log" "$PROJMUX_PROJDIR" "$PROJMUX_MANAGED_ROOTS"
    {
      printf "bind-key -n M-1 run-shell \"%s %s #{client_tty} sessionizer-sidebar\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n M-2 run-shell \"%s %s #{client_tty} notify-sidebar\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n M-3 run-shell \"%s %s #{client_tty} session-popup\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n M-4 run-shell \"%s %s #{client_tty} ai-split-picker-right\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n M-5 run-shell \"%s %s #{client_tty} ai-split-settings\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n M-6 run-shell \"%s %s #{client_tty} sessionizer\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User2 run-shell \"%s %s #{client_tty} notify-sidebar\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User3 run-shell \"%s %s #{client_tty} session-popup\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User4 run-shell \"%s %s #{client_tty} sessionizer-sidebar\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User5 run-shell \"%s %s #{client_tty} ai-split-picker-right\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User6 run-shell \"%s %s #{client_tty} ai-split-settings\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n User12 run-shell \"%s %s #{client_tty} sessionizer\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n C-g run-shell \"%s %s #{client_tty} sessionizer-sidebar\"\n" "$popup_env" "$popup_wrapper"
      printf "bind-key -n C-y run-shell \"%s %s #{client_tty} notify-sidebar\"\n" "$popup_env" "$popup_wrapper"
    } >> "$sandbox_config"
    projmux notify push --text "native no-fzf sandbox notification" --target main --source ai --id poc-native-sandbox >/dev/null
    cd "$demo_root/alpha-api"
    cat <<'"'"'EOF'"'"'

[poc/no-fzf] launching interactive projmux shell

fzf is absent. projmux was built from the mounted POC worktree:
  /usr/local/bin/projmux

Demo project root:
  /workspace/projects

Inside tmux, try:
  Alt-1 / User4: project switch sidebar
  Alt-2 / User2: notification sidebar
  Ctrl-g: project switch sidebar fallback for nested tmux
  Ctrl-y: notification sidebar fallback for nested tmux
  projmux switch
  projmux settings
  tmux show-environment -g PROJMUX_PICKER_BACKEND
  cat /tmp/projmux-popup.log
  projmux doctor --json

Manual UX checks:
  Alt-1 opens with the top border/title visible, not clipped
  searchable pickers show a visible separator between the query line and rows
  vertical borders are continuous while moving Up/Down
  mouse wheel moves selection in native pickers
  primary click applies the clicked row
  Alt-1 closes the sidebar immediately when pressed again
  Alt-2, Alt-3, Alt-4, Alt-5 open their matching native popups and close on
  the same Alt key immediately
  arrow keys move selection without leaking ^[[ text into the query

Environment:
  PROJMUX_PICKER_BACKEND=native
  PROJMUX_PROJDIR=/workspace/projects
  tmux global env also forces native picker for Alt popup bindings
  popup binding stderr/stdout is logged to /tmp/projmux-popup.log

Detach from tmux with Ctrl-b d. Exit the container shell to remove the sandbox.
EOF
    exec projmux shell --socket poc-no-fzf --session main --bin /usr/local/bin/projmux --config "$sandbox_config" --no-install'
