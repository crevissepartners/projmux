# Testing

The local validation contract is exposed through make targets so CI, agents,
and humans run the same entrypoints.

## Targets

- `make test` runs the fast Go unit suite. These tests avoid tmux, TTY, GUI,
  and host shell dependencies.
- `make test-integration` builds `test/docker/Dockerfile` and runs
  `test/integration/linux-smoke.sh` in Docker. It validates Linux dependency
  discovery, tmux config generation/install, app config reload against a real
  `tmux` server, and notify queue CRUD.
- `make test-install-smoke` builds the same Docker image and runs
  `test/install/smoke.sh`. It validates `make install`, atomic binary
  replacement into an isolated install dir, `tmux apply`, and post-install
  `notify reconcile` initialization with a fresh HOME/XDG state tree.
- `make test-e2e` builds the same Docker image and runs
  `test/e2e/linux-smoke.sh`. It validates a minimal real-tmux workflow:
  sessions, panes, config sourcing, reply-state notify reconciliation, focus
  notify fallback, and status notify rendering.

## Docker-Covered Checks

The Docker suites are intended to cover portable Linux behavior that can be
made deterministic in a container:

- binary build and source install into an isolated prefix
- `doctor` dependency checks for `tmux`, `fzf`, `git`, and `stty`
- tmux config print/install/apply paths
- notify queue push/list/ack/reconcile state transitions
- focus fallback behavior when a tmux server has sessions but no attached
  client
- status rendering that only depends on tmux state and local files

The test container disables networking during `docker run`. The image build may
use the network to fetch the pinned base image, apt packages, and pinned fzf
version, but suite execution should not need network access after the image is
built.

## Host-Only Checks

The Docker suites do not replace checks that depend on a real host terminal,
desktop shell, or OS integration:

- terminal emulator key delivery and swallowing for `Alt-1..5`, `Ctrl-N`, and
  CSI-u chords
- Windows Terminal and WSL interop
- macOS host path, shell, and GUI behavior
- desktop notification click callbacks
- terminal-specific popup rendering and interactive key dispatch

Keep those checks as manual or host-run smoke validation until a dedicated
host harness exists.
