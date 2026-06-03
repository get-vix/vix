package brain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

var daemonCtx context.Context

// RegisterBrainHandlers registers brain.* command handlers with the daemon.
func RegisterBrainHandlers(register func(string, func(map[string]any) (map[string]any, error)), cred config.Credential, ctx context.Context) {
	daemonCtx = ctx
	register("brain.init", func(data map[string]any) (map[string]any, error) {
		return doBrainInit(data, cred)
	})
	register("brain.update_files", func(data map[string]any) (map[string]any, error) {
		return doBrainUpdateFiles(data, cred)
	})
}

func doBrainInit(data map[string]any, cred config.Credential) (map[string]any, error) {
	params, _ := data["params"].(map[string]any)
	projectPath, _ := params["project_path"].(string)
	if projectPath == "" {
		projectPath = "."
	}
	root, _ := filepath.Abs(projectPath)

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Not a directory: %s", root)}, nil
	}

	// Resolve brain directory from the caller, falling back to the legacy
	// cwd/.vix layout if unset.
	brainDir, _ := params["brain_dir"].(string)
	if brainDir == "" {
		brainDir = filepath.Join(root, ".vix")
	}
	os.MkdirAll(brainDir, 0o755)

	// Resolve the settings.json paths to consult, falling back to the legacy
	// [home, project] layering if the caller did not supply them.
	var settingsPaths []string
	if raw, ok := params["settings_paths"].([]string); ok {
		settingsPaths = raw
	} else if raw, ok := params["settings_paths"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				settingsPaths = append(settingsPaths, s)
			}
		}
	}
	if len(settingsPaths) == 0 {
		home := config.HomeVixDir()
		if home != "" {
			settingsPaths = append(settingsPaths, filepath.Join(home, "settings.json"))
		}
		settingsPaths = append(settingsPaths, filepath.Join(root, ".vix", "settings.json"))
	}

	// Load language→extension map and initialize LSP pool
	InitLanguageMap(settingsPaths)
	lsp.InitPool(daemonCtx, root, settingsPaths...)

	LogInfo("Brain init complete for %s", root)

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"project_name": filepath.Base(root),
			"brain_path":   brainDir,
		},
	}, nil
}

func doBrainUpdateFiles(data map[string]any, cred config.Credential) (map[string]any, error) {
	return map[string]any{"status": "ok"}, nil
}

