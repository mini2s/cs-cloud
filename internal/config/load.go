package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"cs-cloud/internal/platform"
)

func Load() (*Config, error) {
	cfg := &Config{
		CloudBaseURL: platform.Getenv("CLOUD_BASE_URL"),
		BaseURL:      platform.Getenv("COSTRICT_BASE_URL"),
		DefaultShell: platform.Getenv("CS_CLOUD_SHELL"),
		DefaultAgent: platform.Getenv("CS_CLOUD_DEFAULT_AGENT"),
		AgentCommand: platform.Getenv("CS_CLOUD_AGENT_COMMAND"),
	}

	if cfg.CloudBaseURL == "" {
		cfg.CloudBaseURL = platform.Getenv("COSTRICT_CLOUD_BASE_URL")
	}

	if envJSON := platform.Getenv("CS_CLOUD_AGENT_ENV"); envJSON != "" {
		var env map[string]string
		if err := json.Unmarshal([]byte(envJSON), &env); err == nil {
			cfg.AgentEnv = env
		}
	}

	if env := platform.Getenv("CS_CLOUD_AUTO_UPGRADE"); env != "" {
		cfg.AutoUpgrade = env == "true" || env == "1" || env == "yes"
	}
	if platform.NoAutoUpgrade() {
		cfg.AutoUpgrade = false
	}

	if p, err := configFilePath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			var fileCfg Config
			if err := json.Unmarshal(b, &fileCfg); err == nil {
				if cfg.CloudBaseURL == "" {
					cfg.CloudBaseURL = fileCfg.CloudBaseURL
				}
				if cfg.BaseURL == "" {
					cfg.BaseURL = fileCfg.BaseURL
				}
				if cfg.DefaultShell == "" {
					cfg.DefaultShell = fileCfg.DefaultShell
				}
				if cfg.AgentCommand == "" {
					cfg.AgentCommand = fileCfg.AgentCommand
				}
				if cfg.DefaultAgent == "" {
					cfg.DefaultAgent = fileCfg.DefaultAgent
				}
				if cfg.AgentEnv == nil && fileCfg.AgentEnv != nil {
					cfg.AgentEnv = fileCfg.AgentEnv
				}
				if cfg.AgentWorkspace == "" {
					cfg.AgentWorkspace = fileCfg.AgentWorkspace
				}
			}
		}
	}

	if cfg.DefaultAgent == "" {
		if platform.IsInvokedByCsc() {
			cfg.DefaultAgent = "csc"
		} else {
			cfg.DefaultAgent = "cs"
		}
	}

	return cfg, nil
}

func configFilePath() (string, error) {
	return filepath.Join(platform.AppDir(), "config.json"), nil
}
