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

## 0.4 주요 변경

- **`projmux setup` / `projmux init`** — 터미널이 어떤 키 시퀀스를
  swallow 하는지 진단한 뒤, Ghostty 또는 Windows Terminal 설정에 맞는
  CSI-u 바인딩을 자동 머지한다.
- **`projmux doctor`** — 런타임 의존성 점검 + 최소 버전 강제 (tmux 3.4,
  fzf 0.65.0).
- **`projmux focus`** — AI reply-ready 와 status-bar notify 클릭이 공유하는
  통합 switch-client 디스패처.
- **영속 notify queue** — `projmux notify push|list|ack|reconcile` 가 TTL,
  severity, source, target 메타데이터를 가진 큐를 디스크에 보존한다.
  자세한 내용은 [notify-queue.md](docs/notify-queue.md).
- **Authoritative 사용량 추적** — `projmux usage` 가 Claude OAuth usage
  endpoint 와 Codex 로컬 rollout 의 `rate_limits` 를 직접 읽는다.
  자세한 내용은 [usage-tracking.md](docs/usage-tracking.md).
- **Two-line clickable status bar** — row 0 는 native window list 를 그대로
  살려 탭 클릭으로 전환할 수 있고, row 1 은 좌측 notify HUD 와 우측 usage
  HUD 로 분할된다. 폭이 좁아지면 두 세그먼트 모두 단계적으로 축소된다.
  자세한 내용은 [statusbar.md](docs/statusbar.md).

## 무엇을 하나

- 프로젝트 디렉터리에서 tmux session을 만들거나 기존 session으로 전환.
- 기존 session을 window/pane preview와 함께 탐색.
- `fzf` 기반 popup/sidebar navigation 제공.
- 자주 쓰는 프로젝트 pin 관리와 일반적인 source root 자동 탐색.
- window/pane preview 선택 상태 저장 및 빠른 순환.
- launcher, rename prompt, pane border, status segment, attention hook을 위한
  tmux binding 생성.
- git branch와 Kubernetes context/namespace를 status area에 표시.
- Two-line clickable status bar — row 0 의 native 윈도우 목록은 탭 클릭으로
  바로 전환되고, row 1 은 notify(좌)와 AI usage(우) HUD 세그먼트로 분할된다.
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
- [fzf](https://github.com/junegunn/fzf#installation) **≥ 0.65.0** — popup/sidebar picker. `PATH` 에 있는 `fzf` 실행 파일이 `fzf --version` 에서 0.65.0 이상을 보고해야 합니다. Ubuntu 24 apt 같은 distro package 는 너무 오래됐을 수 있으니 upstream GitHub Releases, Homebrew, 또는 버전을 확인할 수 있는 다른 설치 경로를 사용하세요. `npm i fzf` 는 JavaScript fuzzy-search library 이며 projmux 가 실행하는 junegunn/fzf CLI binary 가 아닙니다.
- `bash`, `zsh`, `sh` 같은 Unix shell — `projmux shell` 이 만드는 앱 tmux
  설정은 절대 경로 `$SHELL` 을 사용하고, 없으면 `/bin/sh` 로 fallback 합니다.
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

`PROJMUX_PROJDIR` 은 명시적으로 설정했을 때 projmux 가 picker/탐색에서 사용할
primary 프로젝트 루트입니다. 설정하지 않으면 projmux 는 canonical repo root 를
가정하지 않습니다. 탐색은 pin, 살아 있는 session, saved workdirs, 그리고 존재할
때만 참고하는 약한 common-folder probe(`~/source`, `~/work`, `~/projects`,
`~/src`, `~/code`) 를 사용합니다.

```sh
export PROJMUX_PROJDIR="/your/path"
```

`~/.bashrc`, `~/.zshrc` 또는 사용 중인 shell rc 파일에 한 줄 추가하면 됩니다. 첫 실행
이후에는 `~/.config/projmux/projdir` 에 memoize 되므로, 이후 env 가 없어도 같은
루트가 유지됩니다.

`PROJMUX_PROJDIR` 은 OS-native PATH 형식의 multi-path 도 받습니다 (Linux/macOS
는 `:`, Windows 는 `;`). 첫 번째 비어있지 않은 항목이 primary 프로젝트 루트가
되고, 이후 항목은 `PROJMUX_MANAGED_ROOTS` 처럼 managed roots 검색 목록 앞에
prepend 됩니다. saved 파일에는 primary 만 memoize 됩니다.

```sh
# Linux/macOS — primary repo + 보조 검색 root
export PROJMUX_PROJDIR="/main/repos:/srv/work/repos"
```

#### 최초 설치 시 projdir 지정

```sh
PROJMUX_PROJDIR=/your/path go install github.com/crevissepartners/projmux/cmd/projmux@latest
PROJMUX_PROJDIR=/your/path projmux tmux apply
```

env 가 살아있는 첫 실행이 `~/.config/projmux/projdir` 에 값을 기록하므로,
이후 새 shell 에서 env 없이도 같은 루트가 유지됩니다.

저장된 값은 `projmux settings > Project Picker > Project Root` 에서도 관리할
수 있습니다. 이 화면은 현재 effective primary root 와 source
(`PROJMUX_PROJDIR`, `@projmux_projdir`, saved, not configured)를 보여주고,
saved 값이 env/tmux 값에 shadow 되는 상황을 따로 표시합니다. 경로를 직접
입력하거나 현재 project context 를 저장하거나 saved 값을 지울 수 있습니다.
Project Root 가 설정되지 않은 경우 직접 입력 prompt 는 `$HOME` 을 수정 가능한
fallback 으로 채웁니다. 저장하기 전까지는 effective root 로 간주하지 않습니다.

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
project root를 합쳐 후보를 만듭니다. 명시적인 검색 root 가 없으면 기본 탐색은
존재하는 경우 `~/source`, `~/work`, `~/projects`, `~/src`, `~/code` 같은 약한
common-folder probe 를 참고합니다. canonical `~/source/repos` root 는 가정하지
않습니다. `projmux settings`의 `Project Picker > Add Project...`는 filesystem
root를 depth 3까지 스캔하므로 약한 probe 밖의 프로젝트도 picker 후보로 추가할
수 있습니다. 세션 이름은 정규화된 디렉터리 경로에서 만들어지므로 같은 프로젝트는
다시 실행해도 같은 tmux 세션으로 연결됩니다.

탐색 root를 영구적으로 커스터마이즈하려면 Project Picker 섹션의 다음 항목을
사용하세요:

- `Project Root` - saved primary root 를 설정/변경/해제합니다. Project Root 는
  primary project context 로 쓰는 한 개의 루트이며, `PROJMUX_PROJDIR` env 와
  tmux `@projmux_projdir` 값이 있으면 saved 값보다 우선됩니다. root 가
  설정되지 않았을 때 직접 입력 prompt 는 `$HOME` 을 미리 채워 picker 를 열 수
  있게 하지만, 저장 전에는 암묵적인 saved root 로 쓰지 않습니다.
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
| `PROJMUX_PROJDIR` | 현재 shell 의 명시적 primary 프로젝트 루트. OS-native PATH 형식 multi-value 지원: 첫 항목이 primary repo root (saved 파일에 memoize), 이후 항목은 managed-roots 검색 목록 앞에 prepend. |
| `PROJMUX_MANAGED_ROOTS` | 콜론 구분 검색 root 목록. saved/heuristic 목록보다 우선. |
| `PROJMUX_NOTIFY_HOOK` | AI desktop notification 을 내장 sender 대신 받는 외부 실행 파일. |
| `PROJMUX_USAGE_STATE_DIR` | AI 사용량 snapshot 캐시 디렉터리. 기본값은 `<state>/projmux/usage`. Dropbox/iCloud 같은 동기화 위치를 가리키게 하면 여러 머신 사이에서 authoritative 사용량을 공유할 수 있다. |
| `PROJMUX_USAGE_DEBUG` | 비어 있지 않으면 `projmux status usage` 의 adapter 오류를 swallow 하지 않고 stderr 로 surface 한다. |
| `PROJMUX_USAGE_LIMITS_PATH` | 사용 중단 (deprecated). limit 값은 upstream API (Anthropic OAuth usage endpoint, Codex `rate_limits`) 에서 직접 가져오므로 이 변수는 읽되 무시된다. |

## AI 사용량 추적

`projmux usage` 는 Claude Code 와 Codex CLI 양쪽의 authoritative 5시간 / 주간
사용률을 보고한다. 두 어댑터 모두 upstream 의 계정 뷰를 직접 읽으므로 `claude
/usage` 와 `codex` 가 native 로 보여주는 숫자와 일치한다:

- **Claude** — `~/.claude/.credentials.json` 의 bearer 토큰으로
  `GET https://api.anthropic.com/api/oauth/usage` 호출. 401 응답 시 한 번의
  refresh round-trip 을 수행해 credentials 파일을 회전된 토큰으로 다시 쓴다.
  토큰은 로그에 기록되지 않는다.
- **Codex** — 가장 최근의 `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` 에서
  마지막 `rate_limits` 페이로드를 읽는다. `primary` 는 5시간 창,
  `secondary` 는 주간 창에 매핑된다.

Snapshot 은 `<state>/projmux/usage/snapshots.json` (또는
`PROJMUX_USAGE_STATE_DIR`) 에 저장되며, tmux 상태바에서 `projmux status
usage` 가 실행될 때 최대 30초 주기로만 refresh 된다.

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
- [CLI Reference](docs/cli.md)
- [Statusbar](docs/statusbar.md)
- [Notify queue](docs/notify-queue.md)
- [Usage tracking](docs/usage-tracking.md)
- [Hooks](docs/hooks.md)
- [Migration Plan](docs/migration-plan.md)
- [Repo Layout](docs/repo-layout.md)
- [터미널 키 설정](docs/keybindings.md)
- [Agent Workflow](docs/agent-workflow.md)

## 라이선스

MIT. [LICENSE](LICENSE)를 참고하세요.
