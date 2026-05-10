# projmux

<p align="center">
  <img src="docs/assets/projmux-icon.png" alt="projmux icon" width="112">
</p>

프로젝트별 tmux workspace를 빠르게 전환하고, preview/status context/AI pane
attention까지 함께 다루는 터미널 workspace 도구입니다.

[![npm version](https://img.shields.io/npm/v/projmux?logo=npm)](https://www.npmjs.com/package/projmux)
[![CI](https://github.com/crevissepartners/projmux/actions/workflows/ci.yml/badge.svg)](https://github.com/crevissepartners/projmux/actions/workflows/ci.yml)

[English README](README.md)

![projmux shell project sidebar demo](docs/assets/projmux-shell-sidebar.gif)

## 무엇인가

`projmux`는 프로젝트 디렉터리를 오래 유지되는 tmux session으로 연결합니다.
프로젝트 전환, session preview, AI split 실행, tmux 안의 상태 표시를 한
키보드 중심 workspace 앱으로 묶습니다.

터미널 workspace를 한 명령으로 열고, 한 세트의 키로 project/window/pane,
notification, settings 사이를 오가고 싶을 때 사용합니다.

## 요구 사항

- [Node.js](https://nodejs.org/)와 npm: 기본 설치 경로에 필요합니다.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) **3.4 이상**.

설치 후 `projmux doctor`로 로컬 runtime을 확인하세요.

## 설치

```sh
npm install -g projmux
projmux version
```

npm package는 작은 Node.js shim과 Linux/macOS x64/arm64용 projmux binary를
설치합니다. 일반 사용자의 기본 배포 경로는 npm입니다.

수동 Go 설치, source checkout, GitHub Release, packaging 세부 사항은
[Install](docs/install.md)을 참고하세요.

## 빠른 시작

격리된 projmux tmux 앱을 엽니다:

```sh
projmux shell
```

앱 안에서:

- `Alt-1`: project sidebar.
- `Alt-2`: notification list.
- `Alt-3`: existing-session picker.
- `Alt-4`: AI split picker.
- `Alt-5`: settings.
- `Alt-6`: project switcher popup.

전체 key map은 [Terminal Keybindings](docs/keybindings.md)를 참고하세요. 키가
동작하지 않으면 tmux 밖에서 `projmux setup`을 실행한 뒤,
지원 터미널에서는 `projmux init [terminal] --apply`를 사용하세요.

## 일상 사용

- project directory를 고르면 projmux가 tmux session을 만들거나 재사용합니다.
- 중요한 project는 pin으로 고정합니다.
- 전환 전에 window, pane, git branch, Kubernetes context, AI pane state를
  preview합니다.
- env var를 직접 편집하지 않고 Settings > Project Picker에서 root와 workdir을
  추가할 수 있습니다.
- Settings > About > Update 또는 `projmux update apply`로 업그레이드합니다.

`PROJMUX_PROJDIR`, managed roots, notification, usage tracking 같은 자세한
설정은 [Configuration](docs/configuration.md)을 참고하세요. installer별 update
동작은 [Upgrading](docs/upgrading.md)에 있습니다.

## 추가 문서

- [Install](docs/install.md)
- [Configuration](docs/configuration.md)
- [Terminal Keybindings](docs/keybindings.md)
- [CLI Reference](docs/cli.md)
- [Statusbar](docs/statusbar.md)
- [Hooks](docs/hooks.md)
- [Usage tracking](docs/usage-tracking.md)
- [Agent Workflow](docs/agent-workflow.md)

## 개발

```sh
make build
make fmt
make fix
make test
```

기여자용 세부 사항은 [Testing](docs/testing.md), [Architecture](docs/architecture.md),
[Repo Layout](docs/repo-layout.md)을 참고하세요.

## 라이선스

MIT. [LICENSE](LICENSE)를 참고하세요.
