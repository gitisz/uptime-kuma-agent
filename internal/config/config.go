package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type LoggingConfig struct {
	Level                string `yaml:"level,omitempty"`                  // debug, info, warn, error
	Format               string `yaml:"format,omitempty"`                 // text, json
	InternalLogDirectory string `yaml:"internal_log_directory,omitempty"` // internal directory for logs and Docker volume mounts
	HostLogDirectory     string `yaml:"host_log_directory,omitempty"`     // host directory for Docker volume mounts
	MaxSize              int    `yaml:"max_size,omitempty"`               // max size in MB before rotation
	MaxAge               int    `yaml:"max_age,omitempty"`                // max age in days
	MaxBackups           int    `yaml:"max_backups,omitempty"`            // max number of backup files
	Compress             *bool  `yaml:"compress,omitempty"`               // compress rotated files
	SocketIOLogLevel     string `yaml:"socketio_log_level,omitempty"`     // debug, info, warn, error, off
}

type ThresholdConfig struct {
	CPU  float64 `yaml:"cpu,omitempty"`
	RAM  float64 `yaml:"ram,omitempty"`
	Disk float64 `yaml:"disk,omitempty"`
}

type AgentConfig struct {
	UseOutputsDiscard *bool         `yaml:"use_outputs_discard,omitempty"`
	DockerImage       string        `yaml:"docker_image"`
	Logging           LoggingConfig `yaml:"logging,omitempty"`
}

type GroupConfig struct {
	Name              string   `yaml:"name"`
	Description       *string  `yaml:"description,omitempty"`
	NotificationNames []string `yaml:"notification_names,omitempty"`
}

type Config struct {
	Version          string          `yaml:"version,omitempty"`
	UptimeKumaURL    string          `yaml:"uptime_kuma_url"`
	Username         string          `yaml:"username"`
	Password         string          `yaml:"password"`
	Groups           []GroupConfig   `yaml:"groups"`
	Interval         int             `yaml:"interval"`
	MaxRetries       int             `yaml:"max_retries"`
	GlobalThresholds ThresholdConfig `yaml:"global_thresholds,omitempty"`
	Agent            AgentConfig     `yaml:"agent,omitempty"`
	PushMonitors     []MonitorConfig `yaml:"push_monitors,omitempty"`
	HTTPMonitors     []MonitorConfig `yaml:"http_monitors,omitempty"`
	// Deprecated: Use PushMonitors and HTTPMonitors instead
	Monitors []MonitorConfig `yaml:"monitors,omitempty"`
}

type MonitorConfig struct {
	Type              string   `yaml:"type"`
	Name              string   `yaml:"name"`
	Group             string   `yaml:"group,omitempty"`
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

	// Merge GlobalThresholds (last config wins)
	if add.GlobalThresholds.CPU > 0 {
		base.GlobalThresholds.CPU = add.GlobalThresholds.CPU
	}
	if add.GlobalThresholds.RAM > 0 {
		base.GlobalThresholds.RAM = add.GlobalThresholds.RAM
	}
	if add.GlobalThresholds.Disk > 0 {
		base.GlobalThresholds.Disk = add.GlobalThresholds.Disk
	}

	// Merge Groups (avoid duplicates by name)
	groupNameMap := make(map[string]bool)
	for _, g := range base.Groups {
		groupNameMap[g.Name] = true
	}
	for _, g := range add.Groups {
		if !groupNameMap[g.Name] {
			base.Groups = append(base.Groups, g)
			groupNameMap[g.Name] = true
		}
	}

	// Merge PushMonitors (avoid duplicates by name + group)
	pushMonitorMap := make(map[string]bool)
	for _, m := range base.PushMonitors {
		key := m.Name + "|" + m.Group
		pushMonitorMap[key] = true
	}
	for _, m := range add.PushMonitors {
		key := m.Name + "|" + m.Group
		if !pushMonitorMap[key] {
			base.PushMonitors = append(base.PushMonitors, m)
			pushMonitorMap[key] = true
		}
	}

	// Merge HTTPMonitors (avoid duplicates by name + group)
	httpMonitorMap := make(map[string]bool)
	for _, m := range base.HTTPMonitors {
		key := m.Name + "|" + m.Group
		httpMonitorMap[key] = true
	}
	for _, m := range add.HTTPMonitors {
		key := m.Name + "|" + m.Group
		if !httpMonitorMap[key] {
			base.HTTPMonitors = append(base.HTTPMonitors, m)
			httpMonitorMap[key] = true
		}
	}

	// Merge legacy Monitors (avoid duplicates by name)
	monitorMap := make(map[string]bool)
	for _, m := range base.Monitors {
		monitorMap[m.Name] = true
	}
	for _, m := range add.Monitors {
		if !monitorMap[m.Name] {
			base.Monitors = append(base.Monitors, m)
			monitorMap[m.Name] = true
		}
	}

	return base
}

func SaveConfig(configPath string, cfg *Config) error {
	// Deduplicate monitors before saving to prevent accumulation of duplicates
	cfg.deduplicateMonitors()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}

// deduplicateMonitors removes duplicate monitors from the config
func (c *Config) deduplicateMonitors() {
	// Deduplicate PushMonitors
	pushMonitorMap := make(map[string]bool)
	var deduplicatedPush []MonitorConfig
	for _, m := range c.PushMonitors {
		key := m.Name + "|" + m.Group
		if !pushMonitorMap[key] {
			deduplicatedPush = append(deduplicatedPush, m)
			pushMonitorMap[key] = true
		}
	}
	c.PushMonitors = deduplicatedPush

	// Deduplicate HTTPMonitors
	httpMonitorMap := make(map[string]bool)
	var deduplicatedHTTP []MonitorConfig
	for _, m := range c.HTTPMonitors {
		key := m.Name + "|" + m.Group
		if !httpMonitorMap[key] {
			deduplicatedHTTP = append(deduplicatedHTTP, m)
			httpMonitorMap[key] = true
		}
	}
	c.HTTPMonitors = deduplicatedHTTP

	// Deduplicate legacy Monitors
	monitorMap := make(map[string]bool)
	var deduplicatedLegacy []MonitorConfig
	for _, m := range c.Monitors {
		if !monitorMap[m.Name] {
			deduplicatedLegacy = append(deduplicatedLegacy, m)
			monitorMap[m.Name] = true
		}
	}
	c.Monitors = deduplicatedLegacy
}

func (m *MonitorConfig) ResolveMetrics(cfg *Config) {
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

	// Default threshold from global_thresholds or fallback to 90
	if m.Threshold == 0 {
		switch m.Metric {
		case "cpu", "docker_container_cpu":
			if cfg.GlobalThresholds.CPU > 0 {
				m.Threshold = cfg.GlobalThresholds.CPU
			} else {
				m.Threshold = 90
			}
		case "mem":
			if cfg.GlobalThresholds.RAM > 0 {
				m.Threshold = cfg.GlobalThresholds.RAM
			} else {
				m.Threshold = 90
			}
		case "disk":
			if cfg.GlobalThresholds.Disk > 0 {
				m.Threshold = cfg.GlobalThresholds.Disk
			} else {
				m.Threshold = 85
			}
		default:
			m.Threshold = 90
		}
	}
}

// GetAllMonitors returns a consolidated list of all monitors (for backward compatibility)
func (c *Config) GetAllMonitors() []MonitorConfig {
	var all []MonitorConfig

	// Add push monitors with type set
	for _, m := range c.PushMonitors {
		m.Type = "push"
		all = append(all, m)
	}

	// Add HTTP monitors with type set
	for _, m := range c.HTTPMonitors {
		m.Type = "http"
		all = append(all, m)
	}

	// Include deprecated Monitors for backward compatibility (they already have type set)
	all = append(all, c.Monitors...)
	return all
}

// ResolveAllMetrics sets smart defaults and thresholds for all monitors using global config
func (c *Config) ResolveAllMetrics() {
	allMonitors := c.GetAllMonitors()
	for i := range allMonitors {
		allMonitors[i].ResolveMetrics(c)
	}
	// Update the slices with resolved metrics
	copy(c.PushMonitors, allMonitors[:len(c.PushMonitors)])
	copy(c.HTTPMonitors, allMonitors[len(c.PushMonitors):len(c.PushMonitors)+len(c.HTTPMonitors)])
	if len(c.Monitors) > 0 {
		copy(c.Monitors, allMonitors[len(c.PushMonitors)+len(c.HTTPMonitors):])
	}
}
