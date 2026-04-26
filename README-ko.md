# projmux

터미널에서 프로젝트별 tmux 세션을 빠르게 만들고 전환하기 위한 도구입니다.

`projmux`는 프로젝트 디렉터리를 안정적인 tmux 세션으로 매핑하고, `fzf`
기반의 전환 UI와 미리보기를 제공합니다. 개인 dotfiles에 묶이지 않도록 필요한
tmux 설정도 직접 생성할 수 있습니다.

[English README](README.md)

## 주요 기능

- 프로젝트 디렉터리에서 tmux 세션을 만들거나 기존 세션으로 전환.
- 기존 세션 목록을 popup으로 보고 window/pane 미리보기.
- `fzf` 기반 popup 및 sidebar UI.
- 자주 쓰는 프로젝트 pin 관리와 tagged session 작업.
- window/pane preview 선택 상태 저장 및 순환.
- tmux popup launcher, attention badge, status bar segment 설정 생성.
- 별도 tmux 서버와 설정으로 동작하는 `projmux shell` 앱 모드.
- 현재 git branch와 Kubernetes context/namespace status bar 표시.
- AI pane split launcher, 상태 표시, desktop notification 헬퍼.

## 요구 사항

- Go 1.24 이상.
- tmux.
- 대화형 picker를 위한 fzf.
- 생성되는 앱 tmux 설정은 zsh를 기본 shell로 사용합니다.
- git branch/status metadata 표시를 위한 git.
- kubectl은 선택 사항이며 Kubernetes status segment가 필요할 때만 사용합니다.

## 설치

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

앱 세션의 하단 좌측 뱃지는 현재 pane의 프로젝트 이름을 보여줍니다. 하단
우측에는 현재 경로, kube segment, git segment, 시간이 표시됩니다.

자주 쓰는 앱 키:

| 키 | 동작 |
| --- | --- |
| `Alt-1` | 프로젝트 sidebar 열기 |
| `Alt-2` | 기존 세션 popup 열기 |
| `Alt-3` | 프로젝트 switcher popup 열기 |
| `Alt-4` | AI split picker 열기 |
| `Alt-5` | AI split settings 열기 |
| `Ctrl-n` | 현재 pane 디렉터리에서 새 tmux window 생성 |
| `Alt-Left/Right/Up/Down` | pane 이동 |
| `Alt-Shift-Left/Right` | 이전/다음 window |
| `Prefix b` | 기존 세션 popup |
| `Prefix f` | 프로젝트 switcher popup |
| `Prefix F` | 프로젝트 sidebar |
| `Prefix g` | 현재 pane 프로젝트 세션으로 이동 |
| `Prefix r` | 오른쪽 AI split 열기 |
| `Prefix l` | 아래쪽 AI split 열기 |

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

로그인 shell 정책, terminal emulator key dispatch, OS별 패키지 설치는 각자의
dotfiles에 두는 편이 좋습니다. `projmux`는 안정적인 앱 entrypoint와 생성 가능한
tmux 설정을 제공합니다.

## 명령어

주요 이동 명령:

```sh
projmux shell
projmux switch [--ui=popup|sidebar]
projmux sessions [--ui=popup|sidebar]
projmux current
```

세션 생명주기:

```sh
projmux attach auto [--keep=N] [--fallback=home|ephemeral]
projmux kill tagged
projmux kill tagged <session>...
projmux prune ephemeral [--keep=N]
```

Pin, tag, preview 상태:

```sh
projmux pin add <dir>
projmux pin remove <dir>
projmux pin toggle <dir>
projmux pin list
projmux tag toggle <name>
projmux tag list
projmux preview select <session> <window> <pane>
projmux preview cycle-window <session> <next|prev>
projmux preview cycle-pane <session> <next|prev>
```

tmux 연동 헬퍼:

```sh
projmux tmux install
projmux tmux install-app
projmux tmux popup-toggle <mode>
projmux attention toggle [pane]
projmux status git [path]
projmux status kube [session]
```

전체 명령은 `projmux help` 또는 `<command> --help`로 확인할 수 있습니다.

## 프로젝트 탐색 방식

`projmux switch`는 pinned directory, 현재 살아 있는 tmux session, 발견된
project root를 합쳐 후보를 만듭니다. 기본 탐색은 존재하는 경우
`~/source/repos` 같은 일반적인 소스 디렉터리를 우선합니다. 세션 이름은 정규화된
디렉터리 경로에서 만들어지므로 같은 프로젝트는 다시 실행해도 같은 tmux 세션으로
연결됩니다.

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

## 역할 경계

`projmux`는 portable한 세션 관리 핵심을 담당합니다. 예를 들어 session naming,
project discovery, pin, preview state, tmux orchestration, status segment,
생성 가능한 tmux binding이 여기에 속합니다.

개인 dotfiles는 로컬 정책을 담당합니다. terminal emulator key dispatch, zsh
startup 정책, 머신별 패키지와 symlink는 dotfiles에 남기는 것이 좋습니다.

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
- [Agent Workflow](docs/agent-workflow.md)

## 라이선스

MIT. [LICENSE](LICENSE)를 참고하세요.
