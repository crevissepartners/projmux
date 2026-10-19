package tmuxconfig
import (
 "bytes"
 "errors"
 "fmt"
 "os"
 "path/filepath"
 "strings"
 "github.com/crevissepartners/projmux/internal/services/keybinding"
)
// Generation supplies the existing loader seams; ResolvePaths can return exact
// captured Paths without reconstructing a parent XDG directory.
type Generation struct {
 HomeDir func() (string,error)
 LookupEnv func(string) string
 ResolvePaths PathsResolver
 ReadFile func(string) ([]byte,error)
 KeymapPaths func() (string,error)
}
func (g Generation) loadKeyBindingCatalog() ([]keybinding.KeyBindingAction,bool,error) {
 return keybinding.LoadMergedKeyBindingCatalog(keybinding.KeymapLoader{HomeDir:g.HomeDir,LookupEnv:g.LookupEnv,ReadFile:g.ReadFile,Path:g.KeymapPaths})
}
func (g Generation) appConfigThemeSource() RenderThemeSource {
 source,err:=ConfigRenderThemeSource(g.HomeDir,g.LookupEnv,"",g.ResolvePaths)
 if err!=nil {return FallbackRenderThemeSource()}
 return source
}
func (g Generation) defaultShell() string { return DefaultInteractiveShell(g.LookupEnv) }

func WriteTmuxAppConfig(config,binaryPath string,g Generation,writeFile func(string,[]byte,os.FileMode)error) (string,error) {
	if writeFile == nil {
		return "", errors.New("configure tmux install-app writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
		return "", fmt.Errorf("create tmux app config directory: %w", err)
	}
	keyBindings, keymapPresent, err := g.loadKeyBindingCatalog()
	if err != nil {
		return "", err
	}
	generated := []byte(g.appConfigThemeSource().TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, g.defaultShell(), LoadStatusbarDecorationSet(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadAIBadgeStyle(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadDesktopNotifyModeForTmuxConfig(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadLiveResourcesMode(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadStatusbarHUDVisibilitySet(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadStatusbarRowOneVisibilitySet(g.HomeDir, g.LookupEnv, g.ResolvePaths), keyBindings, keymapPresent))
	// Apply is also a convergence boundary for the generated artifact. Avoid a
	// truncate/write when the bytes are already exact so repeat apply preserves
	// the file identity and mtime along with registry/tmux no-op behavior. A
	// failed/absent read keeps the historical writer path and its error surface.
	if g.ReadFile != nil {
		if current, readErr := g.ReadFile(config); readErr == nil && bytes.Equal(current, generated) {
			return config, nil
		}
	}
	if err := writeFile(config, generated, 0o644); err != nil {
		return "", fmt.Errorf("write tmux app config: %w", err)
	}
	return config, nil
}

func WriteShellAppConfig(path,binaryPath string,g Generation,writeFile func(string,[]byte,os.FileMode)error) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("shell app config path is required")
	}
	if writeFile == nil {
		return errors.New("configure shell app config writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shell app config directory: %w", err)
	}
	keyBindings, keymapPresent, err := g.loadKeyBindingCatalog()
	if err != nil {
		return err
	}
	if err := writeFile(path, []byte(g.appConfigThemeSource().TmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, g.defaultShell(), LoadStatusbarDecorationSet(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadAIBadgeStyle(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadDesktopNotifyModeForTmuxConfig(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadLiveResourcesMode(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadStatusbarHUDVisibilitySet(g.HomeDir, g.LookupEnv, g.ResolvePaths), LoadStatusbarRowOneVisibilitySet(g.HomeDir, g.LookupEnv, g.ResolvePaths), keyBindings, keymapPresent)), 0o644); err != nil {
		return fmt.Errorf("write shell app config: %w", err)
	}
	return nil
}
