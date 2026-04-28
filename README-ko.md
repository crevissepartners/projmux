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

- [Go 1.24+](https://go.dev/dl/) — binary 설치/빌드에 필요.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) — workspace 런타임.
- [fzf](https://github.com/junegunn/fzf#installation) — popup/sidebar picker.
- [zsh](https://zsh.sourceforge.io/) — `projmux shell` 이 만드는 앱 tmux 설정의 기본 shell.
- [git](https://git-scm.com/downloads) — branch/status segment.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) — 선택, Kubernetes status segment 사용 시에만.

데스크톱 알림: Linux 는 `notify-send`, WSL 은 `powershell.exe` 토스트를 사용합니다.
다른 실행 파일로 보내려면 `PROJMUX_NOTIFY_HOOK` 을 설정하세요.

## 설치

```sh
go install github.com/es5h/projmux/cmd/projmux@latest
```

binary 는 `$(go env GOBIN)` (설정된 경우) 또는 `$(go env GOPATH)/bin`
(기본값 `~/go/bin`) 에 떨어집니다. 해당 디렉터리가 `PATH` 에 있어야 합니다:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

확인:

```sh
projmux version
```

### 선택: `PROJDIR`

`PROJDIR` 은 projmux 가 picker/탐색에서 기본으로 사용할 프로젝트 루트입니다.
설정하지 않으면 projmux 가 내장된 source-root 탐색(`~/source`, `~/work`,
`~/projects`, `~/src`, `~/code`, `~/source/repos`) 으로 자동 fallback 합니다.

```sh
export PROJDIR="$HOME/source/repos"
```

`~/.zshrc` (또는 사용 중인 shell rc 파일) 에 한 줄 추가하면 됩니다. 첫 실행
이후에는 `~/.config/projmux/projdir` 에 memoize 되므로, 이후 env 가 없어도 같은
루트가 유지됩니다.

### 소스에서 빌드

```sh
git clone https://github.com/es5h/projmux.git
cd projmux
make install
```

`make install` 은 빌드 후 `$(go env GOPATH)/bin/projmux` 를 atomically 교체하고
`projmux tmux apply` 를 실행해 동작 중인 `-L projmux` 서버가 즉시 새 binding 을
반영하도록 합니다. 설치 위치는 `INSTALL_DIR=/usr/local/bin` 으로 override.

## 업그레이드

`projmux upgrade`는 `go install`로 binary를 다시 받아 atomically 교체하고
live tmux 설정을 적용합니다. 기본 동작을 끄고 싶다면 flag를 사용하세요:

```sh
projmux upgrade                       # @latest로 현재 binary를 교체하고 apply
projmux upgrade --ref @v0.2.0          # 특정 tag 고정
projmux upgrade --ref @main            # branch tracking
projmux upgrade --target /usr/local/bin/projmux  # 다른 경로 교체
projmux upgrade --no-apply             # 'projmux tmux apply' 생략
projmux upgrade --dry-run              # 실행 없이 단계만 출력
```

upgrade는 호출 shell의 `PROJDIR`/`RP`를 읽어 `~/.config/projmux/projdir`에
memoize 하므로, 새 binary도 같은 프로젝트 루트 컨텍스트를 유지합니다.

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

WSL에서는 내장 sender가 `powershell.exe`를 통해 Windows toast notification을
보냅니다. 이때 `projmux.TmuxCodex` AppUserModelID를 자동으로 등록해 Windows
알림 설정에서 안정적인 표시 이름을 사용하려고 시도합니다.

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

격리된 앱 설정을 다시 쓰고 동작 중인 `-L projmux` 서버에 즉시 source-file까지
적용하려면:

```sh
projmux tmux apply
```

`projmux tmux apply`는 `make install`과 `projmux upgrade`가 binary 교체 직후
호출하는 명령입니다. pin이나 workdir을 바꾼 뒤 동작 중인 서버에 즉시 반영하고
싶을 때 직접 실행해도 안전합니다.

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
projmux tmux apply
projmux tmux popup-toggle <mode>
projmux tmux rename-pane <pane> <title>
projmux attention toggle [pane]
projmux status git [path]
projmux status kube [session]
```

자가 업데이트:

```sh
projmux upgrade [--ref @latest|@vX.Y.Z|@main] [--target /path] [--no-apply] [--dry-run]
```

전체 명령은 `projmux help` 또는 `<command> --help`로 확인할 수 있습니다.

## 릴리스

`v*` tag가 push되면 GitHub Actions가 release archive를 게시합니다. 현재 앱 기준
baseline release는 `v0.2.1`입니다.

## 프로젝트 탐색 방식

`projmux switch`는 pinned directory, 현재 살아 있는 tmux session, 발견된
project root를 합쳐 후보를 만듭니다. 기본 탐색은 존재하는 경우 `~/source`,
`~/work`, `~/projects`, `~/src`, `~/code`, `~/source/repos` 같은 일반적인
소스 디렉터리를 우선합니다. `projmux settings`의 `Project Picker > Add
Project...`는 이 filesystem root를 depth 3까지 스캔하므로 `~`나 `~rp` 밖의
프로젝트도 picker 후보로 추가할 수 있습니다. 세션 이름은 정규화된 디렉터리
경로에서 만들어지므로 같은 프로젝트는 다시 실행해도 같은 tmux 세션으로 연결됩니다.

탐색 root를 영구적으로 커스터마이즈하려면 Project Picker 섹션의 다음 항목을
사용하세요:

- `+ Add Workdir...` - 디렉터리 하나를 saved workdirs 목록에 누적 추가.
- `Workdirs` - 저장된 workdir 검토/삭제. 환경변수 `PROJMUX_MANAGED_ROOTS` /
  `TMUX_SESSIONIZER_ROOTS`가 설정되어 있으면 read-only 행으로 함께 표시되어
  saved 목록 대신 env list가 우선되는 이유를 한눈에 볼 수 있습니다.

`Add Workdir > Type path manually...`를 고르면 파일시스템 스캔을 건너뛰고
경로를 직접 입력할 수 있습니다. 스캔에 부담이 큰 WSL 마운트
(`/mnt/c/Users/...`), 대용량 NFS, 프로젝트별 임시 루트 등에 활용하세요.

저장 파일은 `~/.config/projmux/workdirs`이며, 절대경로 한 줄당 한 항목이고
`#`로 시작하는 줄은 주석으로 무시됩니다. env가 설정되어 있을 때는 무시되며
env가 비었을 때만 사용됩니다.

## 환경 변수

| 변수 | 용도 |
| --- | --- |
| `PROJDIR` | 현재 shell의 sessionizer repo root. `RP`보다 우선. |
| `RP` | legacy repo root env. `PROJDIR`이 비어 있으면 사용. |
| `PROJMUX_MANAGED_ROOTS` | 콜론 구분 검색 root 목록. saved/default보다 우선. |
| `TMUX_SESSIONIZER_ROOTS` | `PROJMUX_MANAGED_ROOTS`의 legacy 별칭. |
| `PROJMUX_NOTIFY_HOOK` | AI desktop notification 외부 실행 파일. |
| `@projmux_projdir` | tmux user-option. 선언적인 `PROJDIR` 소스. `tmux set-option -g @projmux_projdir <path>`로 설정. |

`PROJDIR` 또는 `RP`가 명시적으로 설정되면 `projmux`는 그 값을
`~/.config/projmux/projdir`에 memoize 하여, 이후 env가 없어도 같은 프로젝트
루트가 유지됩니다.

## 설정과 상태 파일

기본 경로는 XDG 규칙을 따릅니다:

- Config: `~/.config/projmux`
- State: `~/.local/state/projmux`
- Cache: `~/.cache/projmux`, tmux 관련 cache는 `~/.cache/tmux`
- Runtime kube session file: 가능하면 `$XDG_RUNTIME_DIR/kube-sessions`

`~/.config/projmux` 아래의 저장 파일:

| 파일 | 내용 |
| --- | --- |
| `pins` | pinned 프로젝트 디렉터리 (한 줄당 한 경로). |
| `tags` | tagged 세션 이름. |
| `projdir` | memoize된 `PROJDIR` 단일 값 (env 사용 시 자동 기록). |
| `workdirs` | 저장된 workdir 목록. settings에서 관리. |
| `tmux.conf` | 격리 `-L projmux` 서버 설정. `projmux tmux install-app` 또는 `projmux tmux apply`가 다시 씀. |

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
