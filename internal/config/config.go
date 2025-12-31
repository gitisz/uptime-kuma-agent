package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type AgentConfig struct {
	UseOutputsDiscard *bool  `yaml:"use_outputs_discard,omitempty"`
	DockerImage       string `yaml:"docker_image"`
}

type Config struct {
	UptimeKumaURL          string          `yaml:"uptime_kuma_url"`
	Username               string          `yaml:"username"`
	Password               string          `yaml:"password"`
	GroupName              string          `yaml:"group_name"`
	GroupDescription       *string         `yaml:"group_description,omitempty"`
	GroupNotificationNames []string        `yaml:"group_notification_names,omitempty"`
	Interval               int             `yaml:"interval"`
	MaxRetries             int             `yaml:"max_retries"`
	Agent                  AgentConfig     `yaml:"agent,omitempty"`
	Monitors               []MonitorConfig `yaml:"monitors"`
}

type MonitorConfig struct {
	Type              string   `yaml:"type"`
	Name              string   `yaml:"name"`
	Description       *string  `yaml:"description,omitempty"`
	NotificationNames []string `yaml:"notification_names,omitempty"`
	URL               string   `yaml:"url,omitempty"`
	Threshold         float64  `yaml:"threshold,omitempty"` // ← Change to float64
	Metric            string   `yaml:"metric,omitempty"`
	Field             string   `yaml:"field,omitempty"`
	Filesystem        string   `yaml:"filesystem,omitempty"`
	ContainerName     string   `yaml:"container_name,omitempty"`
	PushToken         string   `yaml:"push_token,omitempty"`
}

func LoadMergedConfig(dir string) (*Config, error) {
	// Load base config
	baseFile := filepath.Join(dir, "config.yaml")
	baseData, err := os.ReadFile(baseFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read base config: %w", err)
	}

	var baseConfig Config
	if err := yaml.Unmarshal(baseData, &baseConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal base config: %w", err)
	}

	// Find additional config files
	additionalFiles, err := filepath.Glob(filepath.Join(dir, "config.*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(additionalFiles) // Merge in consistent order

	for _, file := range additionalFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", file, err)
		}

		var addConfig Config
		if err := yaml.Unmarshal(data, &addConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %s: %w", file, err)
		}

		// Merge addConfig into baseConfig
		baseConfig = mergeConfigs(baseConfig, addConfig)
	}

	return &baseConfig, nil
}

func mergeConfigs(base, add Config) Config {
	// Merge simple fields (last wins)
	if add.UptimeKumaURL != "" {
		base.UptimeKumaURL = add.UptimeKumaURL
	}
	if add.Username != "" {
		base.Username = add.Username
	}
	if add.Password != "" {
		base.Password = add.Password
	}
	if add.GroupName != "" {
		base.GroupName = add.GroupName
	}
	if add.GroupDescription != nil {
		base.GroupDescription = add.GroupDescription
	}
	if len(add.GroupNotificationNames) > 0 {
		base.GroupNotificationNames = append(base.GroupNotificationNames, add.GroupNotificationNames...)
	}
	if add.Interval > 0 {
		base.Interval = add.Interval
	}
	if add.MaxRetries > 0 {
		base.MaxRetries = add.MaxRetries
	}

	// Merge Agent
	if add.Agent.UseOutputsDiscard != nil {
		base.Agent.UseOutputsDiscard = add.Agent.UseOutputsDiscard
	}
	if add.Agent.DockerImage != "" {
		base.Agent.DockerImage = add.Agent.DockerImage
	}

	// Append Monitors
	base.Monitors = append(base.Monitors, add.Monitors...)

	return base
}

func SaveConfig(configPath string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}

func (m *MonitorConfig) ResolveMetrics() {
	lowerName := strings.ToLower(m.Name)

	// Smart defaults if not explicitly set
	if m.Metric == "" {
		if strings.Contains(lowerName, "cpu") {
			m.Metric = "cpu"
			if m.Field == "" {
				m.Field = "usage_user" // or "usage_system + usage_user"
			}
		} else if strings.Contains(lowerName, "ram") || strings.Contains(lowerName, "mem") {
			m.Metric = "mem"
			if m.Field == "" {
				m.Field = "used_percent"
			}
		} else if strings.Contains(lowerName, "disk") {
			m.Metric = "disk"
			if m.Field == "" {
				m.Field = "used_percent"
			}
			// Auto-detect filesystem from name if not set
			if m.Filesystem == "" {
				if strings.Contains(lowerName, "root") {
					m.Filesystem = "/"
				} else if strings.Contains(lowerName, "data") {
					m.Filesystem = "/mnt/data/uptime-kuma-test"
				}
			}
		}
	}

	// Default threshold
	if m.Threshold == 0 {
		m.Threshold = 90
	}
}
