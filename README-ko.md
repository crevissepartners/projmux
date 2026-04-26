# projmux

터미널에서 프로젝트별 tmux 작업 공간을 만들고 유지하는 도구입니다.

`projmux`는 프로젝트 디렉터리를 오래 유지되는 tmux workspace로 매핑하고,
preview, sidebar navigation, 생성된 keybinding, status metadata, AI pane
attention signal을 함께 제공합니다. 자체 tmux 앱(`projmux shell`)으로 실행할
수도 있고, 기존 tmux 서버에 같은 기능을 설치할 수도 있습니다.

[English README](README.md)

## 왜 projmux인가

많은 tmux project switcher는 "디렉터리를 고르고 세션에 붙는다"에서 멈춥니다.
`projmux`는 그 흐름을 기본값으로 삼고, 매일 쓰는 터미널 workspace에 필요한
앱 수준의 요소를 더합니다.

- **프로젝트 정체성이 안정적으로 유지됩니다.** directory, pin, live session,
  preview selection, lifecycle command가 같은 session model을 공유합니다.
- **전환하기 전에 맥락을 볼 수 있습니다.** popup과 sidebar에서 session,
  window, pane, git branch, Kubernetes context, pane metadata를 미리 봅니다.
- **tmux layer를 손으로 이어 붙이지 않습니다.** popup launcher, window/pane
  rename, status segment, pane border, attention badge, app mode에 필요한 tmux
  설정을 `projmux`가 생성합니다.
- **AI pane을 일급 workspace 요소로 다룹니다.** Codex/Claude pane을 실행하고,
  agent 이름, topic, thinking/waiting 상태, pane/window/session badge, desktop
  notification까지 tmux 안에서 추적합니다.
- **격리 실행과 기존 tmux 통합 중 선택할 수 있습니다.** `projmux shell`로 자체
  tmux 앱을 쓰거나, 생성된 snippet을 기존 tmux 서버에 설치할 수 있습니다.

## 무엇을 하나

- 프로젝트 디렉터리에서 tmux session을 만들거나 기존 session으로 전환.
- 기존 session을 window/pane preview와 함께 탐색.
- `fzf` 기반 popup/sidebar navigation 제공.
- 자주 쓰는 프로젝트 pin 관리와 일반적인 source root 자동 탐색.
- window/pane preview 선택 상태 저장 및 빠른 순환.
- launcher, rename prompt, pane border, status segment, attention hook을 위한
  tmux binding 생성.
- git branch와 Kubernetes context/namespace를 status area에 표시.
- Codex/Claude/plain shell split을 만들고 agent/topic/status/notification 상태를
  tmux UI에 표시.

## 일반적인 사용 흐름

```sh
projmux shell
```

앱을 한 번 실행한 뒤, 생성된 tmux binding으로 다음 일을 합니다:

- sidebar 또는 popup에서 프로젝트 이동,
- attach 전에 session 내용을 미리 확인,
- 현재 workspace에 Codex, Claude, plain shell split 추가,
- window와 AI pane topic rename,
- 확인이 필요한 pane을 badge와 desktop notification으로 파악.

## 요구 사항

- Go 1.24 이상.
- tmux.
- 대화형 picker를 위한 fzf.
- 생성되는 앱 tmux 설정은 zsh를 기본 shell로 사용합니다.
- git branch/status metadata 표시를 위한 git.
- kubectl은 선택 사항이며 Kubernetes status segment가 필요할 때만 사용합니다.
- Linux desktop notification은 기본적으로 `notify-send`를 사용하며,
  `PROJMUX_NOTIFY_HOOK`을 설정하면 별도 실행 파일로 보낼 수 있습니다.

## 설치

Go로 설치:

```sh
GOBIN="$HOME/.local/bin" go install github.com/es5h/projmux/cmd/projmux@latest
hash -r
```

기본적으로 `go install`은 binary를 `$(go env GOPATH)/bin`에 씁니다. 이 위치가
shell이 `PATH`에서 먼저 찾는 `projmux`와 다를 수 있으므로, 설치나 업데이트 후에도
실제로 실행되는 `projmux`가 바뀌지 않을 수 있습니다. 위 명령은 `~/.local/bin`이
이미 `PATH`에 들어 있다는 전제에서 그 위치에 직접 설치하는 권장 방식입니다.
`hash -r`은 업데이트 후 shell의 command lookup cache를 지웁니다.

`~/.local/bin`이 `PATH`에 들어 있어야 합니다:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

대안으로 `$(go env GOPATH)/bin`을 `PATH`에 추가하거나,
`$(go env GOPATH)/bin/projmux` binary를 `PATH`에서 더 앞에 있는 디렉터리로
복사할 수 있습니다.

소스에서 빌드:

```sh
git clone https://github.com/es5h/projmux.git
cd projmux
make build
install -m 0755 .bin/projmux ~/.local/bin/projmux
```

`~/.local/bin`이 `PATH`에 들어 있어야 합니다:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

설치 확인:

```sh
projmux version
projmux help
```

AI desktop notification은 custom executable로 보낼 수 있습니다:

```sh
export PROJMUX_NOTIFY_HOOK="$HOME/.local/bin/projmux-notify"
```

hook은 summary, body, urgency, app name, tag, group, icon path 순서로 7개
인자를 받습니다. 이 변수가 설정되어 있으면 projmux는 내장 desktop notification
sender 대신 hook을 사용합니다.

여기까지면 standalone 앱 경로를 사용할 준비가 끝납니다. `projmux shell`은 자체
tmux 설정을 생성하므로 기존 `.tmux.conf` include나 zsh framework가 필요하지
않습니다.

개발 중에는 다음 명령을 사용합니다:

```sh
make fmt
make fix
make test
make test-integration
make test-e2e
```

## 빠른 시작

격리된 projmux tmux 앱을 실행합니다:

```sh
projmux shell
```

이 명령은 `~/.config/projmux/tmux.conf`를 만들고 다음 형태로 tmux를
실행합니다:

```sh
tmux -L projmux -f ~/.config/projmux/tmux.conf new-session -A -s main
```

이 경로가 권장되는 첫 실행 진입점입니다. projmux가 이 tmux 서버, 생성된 설정,
status bar, popup binding을 직접 소유합니다.

앱 세션의 하단 좌측 뱃지는 현재 pane의 프로젝트 이름을 보여줍니다. 하단
우측에는 현재 경로, kube segment, git segment, 시간이 표시됩니다.

자주 쓰는 앱 키는 projmux가 생성한 tmux 설정에 들어 있습니다. 터미널 에뮬레이터에서
키 전달을 명시해야 한다면 [터미널 키 설정](docs/keybindings.md)을 참고하세요.

## 기존 tmux에 적용

격리된 앱 서버가 아니라 평소 쓰는 tmux 서버에 projmux를 붙이고 싶다면 생성된
tmux snippet을 설치합니다:

```sh
projmux tmux install --bin "$(command -v projmux)"
tmux source-file ~/.tmux.conf
```

이 명령은 `~/.config/tmux/projmux.conf`를 쓰고, `~/.tmux.conf`에 source
라인을 추가합니다. 설치된 binding은 shell script wrapper 없이 `projmux`를 직접
호출합니다.

설치 전에 생성될 설정을 확인하려면:

```sh
projmux tmux print-config --bin "$(command -v projmux)"
```

격리된 앱 설정만 다시 생성하려면:

```sh
projmux tmux install-app --bin "$(command -v projmux)"
```

## zsh 적용

가장 단순한 방법은 alias입니다:

```sh
alias pmx='projmux shell'
```

새 interactive zsh가 자동으로 projmux 앱에 들어가게 하려면 `~/.zshrc`에
guarded hook을 추가할 수 있습니다:

```sh
if [[ -o interactive && -z "${TMUX:-}" ]] && command -v projmux >/dev/null 2>&1; then
  exec projmux shell
fi
```

`projmux`는 안정적인 앱 entrypoint와 생성 가능한 tmux 설정을 제공합니다.

## 명령어

주요 이동 명령:

```sh
projmux shell
projmux switch [--ui=popup|sidebar]
projmux sessions [--ui=popup|sidebar]
projmux settings
projmux current
```

세션 생명주기:

```sh
projmux attach auto [--keep=N] [--fallback=home|ephemeral]
projmux prune ephemeral [--keep=N]
```

Pin과 preview 상태:

```sh
projmux pin add <dir>
projmux pin remove <dir>
projmux pin toggle <dir>
projmux pin list
projmux preview select <session> <window> <pane>
projmux preview cycle-window <session> <next|prev>
projmux preview cycle-pane <session> <next|prev>
```

tmux 연동 헬퍼:

```sh
projmux tmux install
projmux tmux install-app
projmux tmux popup-toggle <mode>
projmux tmux rename-pane <pane> <title>
projmux attention toggle [pane]
projmux status git [path]
projmux status kube [session]
```

전체 명령은 `projmux help` 또는 `<command> --help`로 확인할 수 있습니다.

## 릴리스

`v*` tag가 push되면 GitHub Actions가 release archive를 게시합니다. 현재 앱 기준
baseline release는 `v0.1.1`입니다.

## 프로젝트 탐색 방식

`projmux switch`는 pinned directory, 현재 살아 있는 tmux session, 발견된
project root를 합쳐 후보를 만듭니다. 기본 탐색은 존재하는 경우 `~/source`,
`~/work`, `~/projects`, `~/src`, `~/code`, `~/source/repos` 같은 일반적인
소스 디렉터리를 우선합니다. `projmux settings`의 `Project Picker > Add
Project...`는 이 filesystem root를 depth 3까지 스캔하므로 `~`나 `~rp` 밖의
프로젝트도 picker 후보로 추가할 수 있습니다. 세션 이름은 정규화된 디렉터리
경로에서 만들어지므로 같은 프로젝트는 다시 실행해도 같은 tmux 세션으로 연결됩니다.

## 설정과 상태 파일

기본 경로는 XDG 규칙을 따릅니다:

- Config: `~/.config/projmux`
- State: `~/.local/state/projmux`
- Cache: `~/.cache/projmux`, tmux 관련 cache는 `~/.cache/tmux`
- Runtime kube session file: 가능하면 `$XDG_RUNTIME_DIR/kube-sessions`

생성된 앱 tmux 설정:

```text
~/.config/projmux/tmux.conf
```

생성된 일반 tmux snippet:

```text
~/.config/tmux/projmux.conf
```

## 범위

`projmux`는 portable한 세션 관리 핵심을 담당합니다. 예를 들어 session naming,
project discovery, pin, preview state, tmux orchestration, status segment,
생성 가능한 tmux binding이 여기에 속합니다.

## 개발

자주 쓰는 명령:

```sh
make build
make fmt
make fix
make test
make test-integration
make test-e2e
make verify
```

추가 문서:

- [Architecture](docs/architecture.md)
- [CLI Shape](docs/cli.md)
- [Migration Plan](docs/migration-plan.md)
- [Repo Layout](docs/repo-layout.md)
- [터미널 키 설정](docs/keybindings.md)
- [Agent Workflow](docs/agent-workflow.md)

## 라이선스

MIT. [LICENSE](LICENSE)를 참고하세요.
