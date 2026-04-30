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
- [tmux](https://github.com/tmux/tmux/wiki/Installing) **≥ 3.4** — workspace 런타임. 이전 버전은 `display-popup -T` 등 projmux 가 사용하는 기능이 없습니다.
- [fzf](https://github.com/junegunn/fzf#installation) **≥ 0.55** — popup/sidebar picker. 멀티라인 피커가 `--marker-multi-line`, `--gap-line`, `--highlight-line` 을 사용하며 모두 0.55 까지 도입됐습니다.
- [zsh](https://zsh.sourceforge.io/) — `projmux shell` 이 만드는 앱 tmux 설정의 기본 shell.
- [git](https://git-scm.com/downloads) — branch/status segment.
- `stty` — POSIX 터미널 제어, `projmux setup` 에서 사용. macOS/Linux 기본 시스템에 이미 포함, Windows 호스트에선 해당 없음.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) — 선택, Kubernetes status segment 사용 시에만.

데스크톱 알림: Linux 는 `notify-send`, WSL 은 `powershell.exe` 토스트를 사용합니다.
다른 실행 파일로 보내려면 `PROJMUX_NOTIFY_HOOK` 을 설정하세요.

`projmux doctor` 를 실행하면 위 의존성이 모두 PATH 에 있는지, tmux/fzf 가
최소 지원 버전을 만족하는지 한 번에 확인할 수 있습니다.

## 설치

```sh
go install github.com/crevissepartners/projmux/cmd/projmux@latest
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

### 선택: `PROJMUX_PROJDIR`

`PROJMUX_PROJDIR` 은 projmux 가 picker/탐색에서 기본으로 사용할 프로젝트
루트입니다. 설정하지 않으면 projmux 가 내장된 source-root 탐색(`~/source`,
`~/work`, `~/projects`, `~/src`, `~/code`, `~/source/repos`) 으로 자동
fallback 합니다.

```sh
export PROJMUX_PROJDIR="$HOME/source/repos"
```

`~/.zshrc` (또는 사용 중인 shell rc 파일) 에 한 줄 추가하면 됩니다. 첫 실행
이후에는 `~/.config/projmux/projdir` 에 memoize 되므로, 이후 env 가 없어도 같은
루트가 유지됩니다.

`PROJMUX_PROJDIR` 은 OS-native PATH 형식의 multi-path 도 받습니다 (Linux/macOS
는 `:`, Windows 는 `;`). 첫 번째 비어있지 않은 항목이 primary 프로젝트 루트가
되고, 이후 항목은 `PROJMUX_MANAGED_ROOTS` 처럼 managed roots 검색 목록 앞에
prepend 됩니다. saved 파일에는 primary 만 memoize 됩니다.

```sh
# Linux/macOS — primary repo + 보조 검색 root
export PROJMUX_PROJDIR="$HOME/source/repos:/srv/work/repos"
```

#### 최초 설치 시 projdir 지정

```sh
PROJMUX_PROJDIR=/your/path go install github.com/crevissepartners/projmux/cmd/projmux@latest
PROJMUX_PROJDIR=/your/path projmux tmux apply
```

env 가 살아있는 첫 실행이 `~/.config/projmux/projdir` 에 값을 기록하므로,
이후 새 shell 에서 env 없이도 같은 루트가 유지됩니다.

### 소스에서 빌드

```sh
git clone https://github.com/crevissepartners/projmux.git
cd projmux
make install
```

`make install` 은 빌드 후 `$(go env GOPATH)/bin/projmux` 를 atomically 교체하고
`projmux tmux apply` 를 실행해 동작 중인 `-L projmux` 서버가 즉시 새 binding 을
반영하도록 합니다. 설치 위치는 `INSTALL_DIR=/usr/local/bin` 으로 override.

## 빠른 시작

격리된 projmux tmux 앱을 실행합니다:

```sh
projmux shell
```

projmux가 이 tmux 서버, 생성된 설정, status bar, popup binding을 직접 소유합니다.
하단 좌측 뱃지에는 현재 프로젝트 이름이, 우측에는 경로/kube/git/시간이 표시됩니다.

키가 동작하지 않으면 `projmux setup` 으로 터미널이 어떤 시퀀스를 swallow 하는지
진단한 뒤, `projmux init [terminal] --apply` (terminal 생략 시 자동 감지) 로
필요한 CSI-u 바인딩을 터미널 설정에 머지하세요. 여러 머신을 dotfiles 로
관리하는 경우에는 `--allow-symlink` 또는 `--config <path>` 로 의도를 명시합니다.
전체 흐름과 수동 CSI-u fallback 은 [터미널 키 설정](docs/keybindings.md) 참고.

뭔가 동작이 이상하면 `projmux doctor` 가 어떤 의존성이 누락/구버전인지와
설치 방법을 알려줍니다. 지원 버전은 [요구 사항](#요구-사항) 참고.

## 업그레이드

`projmux upgrade` 는 `go install` 로 binary 를 다시 받아 atomically 교체하고,
동작 중인 `-L projmux` 서버에 라이브 tmux 설정까지 다시 적용합니다.

```sh
projmux upgrade                                  # @latest 로 교체 + apply
projmux upgrade --ref @v0.2.0                    # 특정 tag 고정
projmux upgrade --ref @main                      # branch tracking
projmux upgrade --target /usr/local/bin/projmux  # 다른 경로 교체
projmux upgrade --no-apply                       # 'projmux tmux apply' 생략
projmux upgrade --dry-run                        # 실행 없이 단계만 출력
```

upgrade 는 호출 shell 의 `PROJMUX_PROJDIR` 을 읽어 primary (첫 번째) 경로를
`~/.config/projmux/projdir` 에 memoize 하므로, 새 binary 도 같은 프로젝트 루트
컨텍스트를 유지합니다.

업그레이드와 동시에 새 프로젝트 루트로 전환하고 싶다면 env 와 함께 호출하세요:

```sh
PROJMUX_PROJDIR=/new/path projmux upgrade

# multi-path 도 동일하게 동작합니다. saved 파일에는 primary 만 기록됩니다.
PROJMUX_PROJDIR="/main/repos:/secondary/repos" projmux upgrade   # Linux/macOS
# Windows: PROJMUX_PROJDIR="C:\main\repos;C:\secondary\repos"
```

## 사용법

일상 작업은 `projmux shell` 안의 tmux 키바인딩으로 진행합니다 —
[터미널 키 설정](docs/keybindings.md) 참고. pin / preview / status helper /
`upgrade` 같은 CLI 전체 표면은 `projmux help` 또는 `<command> --help` 로 확인할
수 있습니다.

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

## Hooks

projmux는 새 tmux 세션을 만들 때마다 선택적 사용자 스크립트
`~/.config/projmux/hooks/post-create`를 실행합니다. `tmux set-environment`로
세션별 환경 변수를 주입하거나(예: 프로젝트 경로별 `GH_TOKEN` 선택) 다른
부수 효과를 걸 때 활용하세요. 파일이 없거나 실행 비트가 빠져 있으면 조용히
건너뛰며, hook 실패는 세션 생성을 막지 않습니다. 환경 변수 계약, 예시,
문제 해결은 [Hooks](docs/hooks.md)를 참고하세요.

## 환경 변수

| 변수 | 용도 |
| --- | --- |
| `PROJMUX_PROJDIR` | 현재 shell 의 기본 프로젝트 루트. OS-native PATH 형식 multi-value 지원: 첫 항목이 primary repo root (saved 파일에 memoize), 이후 항목은 managed-roots 검색 목록 앞에 prepend. |
| `PROJMUX_MANAGED_ROOTS` | 콜론 구분 검색 root 목록. saved/default 보다 우선. |
| `PROJMUX_NOTIFY_HOOK` | AI desktop notification 을 내장 sender 대신 받는 외부 실행 파일. |

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
- [Hooks](docs/hooks.md)
- [Migration Plan](docs/migration-plan.md)
- [Repo Layout](docs/repo-layout.md)
- [터미널 키 설정](docs/keybindings.md)
- [Agent Workflow](docs/agent-workflow.md)

## 라이선스

MIT. [LICENSE](LICENSE)를 참고하세요.
