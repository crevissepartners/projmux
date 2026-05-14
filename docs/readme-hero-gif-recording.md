# README Hero GIF Recording

This recipe records the README hero GIF:

- `docs/assets/projmux-ai-attention.gif`

The maintained recorder is kept in the local dotfiles checkout at:

```sh
/home/es5h/dotfiles/bin/projmux/projmux-record-readme-gifs.py
```

## Prerequisites

Install or verify these local tools before recording:

- `python3`
- `git`
- `tmux`
- `ffmpeg` and `ffprobe`
- `Xvfb`
- `openbox`
- `ghostty`
- `xdotool`
- `xwininfo`
- `script` from util-linux
- authenticated `codex`

Build the local projmux binary first:

```sh
make build
```

The script expects `.bin/projmux` by default. Override paths only when needed:

```sh
PROJMUX_RECORD_REPO=/path/to/projmux \
PROJMUX_RECORD_BIN=/path/to/projmux \
PROJMUX_RECORD_DISPLAY=117 \
PROJMUX_RECORD_SCREEN=2560x1440x24 \
PROJMUX_RECORD_KEEP_TMP=1 \
python3 /home/es5h/dotfiles/bin/projmux/projmux-record-readme-gifs.py
```

For the normal checkout, run:

```sh
python3 /home/es5h/dotfiles/bin/projmux/projmux-record-readme-gifs.py
```

## Scenario Contract

The recording uses a private demo home, XDG config/state dirs, demo git
projects, and an isolated `CODEX_HOME`. It copies the local Codex auth/config
into that isolated home, trusts the demo projects/hooks, and seeds usage cache
data so the tmux status line shows the Codex HUD during the capture.

The scenario records the AI attention flow:

1. Start in `mobile-client` with no pending notification.
2. Open the AI picker and launch a real Codex pane.
3. Ask Codex to do a short task.
4. Use the projmux sessionizer sidebar to move to `atlas-api`.
5. Keep working in zsh while Codex finishes in the previous project.
6. When the Codex completion notification exists, open the notification sidebar.
7. Select the notification and return focus to the original Codex pane.

## Visual Guardrails

- Keep the native picker UI native. The script runs picker commands through
  `script(1)` with a fixed PTY size so fzf/terminal UI rendering is captured
  instead of degraded line-mode output.
- Keep the terminal in zsh with the demo prompt so the project and git branch
  are visible.
- Keep the Codex usage HUD visible in the tmux status line.
- Use the sessionizer sidebar for project movement in 4a.
- Capture the Ghostty X11 window geometry with `xwininfo` and feed that exact
  rectangle to ffmpeg. This prevents the GIF from drifting away from `(0, 0)`.

## Verification

After recording, inspect the resulting streams:

```sh
ffprobe -v error -select_streams v:0 \
  -show_entries stream=width,height,nb_frames,duration \
  -of default=noprint_wrappers=1 \
  docs/assets/projmux-ai-attention.gif
```

Expected final shape is roughly 1110px wide and about 16-18 seconds, with the
native picker, zsh prompt, Codex pane, notification sidebar, and usage HUD
visible in the relevant frames.

Set `PROJMUX_RECORD_KEEP_TMP=1` when you need to inspect intermediate MP4s,
Ghostty logs, or palette files under `/tmp/projmux-readme-record-*`.
