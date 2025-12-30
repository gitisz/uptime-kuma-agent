package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	kuma "github.com/breml/go-uptime-kuma-client"
	"github.com/breml/go-uptime-kuma-client/monitor"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	configPath   string
	telegrafDir  = "/etc/telegraf/telegraf.d"
	withTelegraf bool
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

//go:embed templates/*.tmpl
var templateFS embed.FS
var outputsDiscardTmpl *template.Template // <-- add this
var invalidChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var multipleHyphens = regexp.MustCompile(`-+`) // Match one or more hyphens

func init() {
	// Default to the Docker-mounted path
	telegrafDir = "/telegraf.d"
	var err error
	outputsDiscardTmpl, err = template.ParseFS(templateFS, "templates/outputs_discard.tmpl")
	if err != nil {
		panic(err)
	}
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "uptime-kuma-agent",
		Short: "Uptime Kuma provisioning agent",
		Run: func(cmd *cobra.Command, args []string) {
			if err := run(); err != nil {
				log.Fatal(err)
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&configPath, "config", "/config/config.yaml", "path to config file")
	rootCmd.PersistentFlags().BoolVar(&withTelegraf, "with-telegraf", true, "generate Telegraf configuration files")
	rootCmd.PersistentFlags().StringVar(&telegrafDir, "telegraf-dir", "/telegraf.d", "Directory to write Telegraf drop-in configs")

	// Add push-metric subcommand: ./uptime-kuma-agent push-metric --monitor "<monitor-name>" --token "<token>"
	rootCmd.AddCommand(pushMetricCmd)
	pushMetricCmd.Flags().String("monitor", "", "Monitor name")
	pushMetricCmd.Flags().String("token", "", "Push token")
	pushMetricCmd.MarkFlagRequired("monitor")
	pushMetricCmd.MarkFlagRequired("token")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadMergedConfig(filepath.Dir(configPath))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := kuma.New(ctx, cfg.UptimeKumaURL, cfg.Username, cfg.Password, kuma.WithLogLevel(kuma.LogLevelDebug))
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	log.Println("Client created successfully")
	defer client.Disconnect()

	if err := provisionKumaMonitor(ctx, client, cfg); err != nil {
		return err
	}
	log.Println("Provisioning completed successfully")

	// Force immediate exit to avoid hanging on Socket.IO goroutines
	os.Exit(0)

	return nil
}

func loadMergedConfig(dir string) (*Config, error) {
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

func saveConfig(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}

func (m *MonitorConfig) resolveMetrics() {
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

func generatePushToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func sanitizeFilename(name string) string {
	// 1. To lowercase
	name = strings.ToLower(name)

	// 2. Replace any sequence of invalid chars with a single hyphen
	name = invalidChars.ReplaceAllString(name, "-")

	// 3. Collapse multiple hyphens into one
	name = multipleHyphens.ReplaceAllString(name, "-")

	// 4. Truncate to max length (before final trim)
	if len(name) > 50 {
		name = name[:50]
	}

	// 5. Trim leading/trailing hyphens (NOT underscores!)
	name = strings.Trim(name, "-")

	// 6. Fallback if name became empty
	if name == "" {
		name = "monitor"
	}

	return name
}

// Add this function anywhere in your file (e.g., near provisioning logic)
func resolveNotificationIDs(ctx context.Context, client *kuma.Client, names []string) ([]int64, error) {
	if len(names) == 0 {
		return nil, nil
	}

	notifications := client.GetNotifications(ctx)

	nameToID := make(map[string]int64)
	for _, n := range notifications {
		nameToID[n.Name] = n.ID
	}

	var ids []int64
	missing := make([]string, 0)
	for _, name := range names {
		if id, found := nameToID[name]; found {
			ids = append(ids, id)
		} else {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		log.Printf("Warning: notification names not found: %v", missing)
	}

	return ids, nil
}

func updateMonitorBase(ctx context.Context, client *kuma.Client, monID int64, mcfg *MonitorConfig, groupNotificationIDs []int64) error {
	updated := false

	switch mcfg.Type {
	case "push":
		var push monitor.Push
		if err := client.GetMonitorAs(ctx, monID, &push); err != nil {
			return fmt.Errorf("failed to fetch push monitor %d: %w", monID, err)
		}

		if push.Base.Description == nil || (mcfg.Description != nil && *push.Base.Description != *mcfg.Description) {
			push.Base.Description = mcfg.Description
			updated = true
		}

		targetIDs := groupNotificationIDs
		if len(mcfg.NotificationNames) > 0 {
			ids, err := resolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			targetIDs = ids
		}

		if !reflect.DeepEqual(push.Base.NotificationIDs, targetIDs) {
			push.Base.NotificationIDs = targetIDs
			updated = true
		}

		if updated {
			if err := client.UpdateMonitor(ctx, &push); err != nil {
				return fmt.Errorf("failed to update push monitor %d: %w", monID, err)
			}
		}

	case "http":
		var httpMon monitor.HTTP
		if err := client.GetMonitorAs(ctx, monID, &httpMon); err != nil {
			return fmt.Errorf("failed to fetch http monitor %d: %w", monID, err)
		}

		if httpMon.Base.Description == nil || (mcfg.Description != nil && *httpMon.Base.Description != *mcfg.Description) {
			httpMon.Base.Description = mcfg.Description
			updated = true
		}

		targetIDs := groupNotificationIDs
		if len(mcfg.NotificationNames) > 0 {
			ids, err := resolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			targetIDs = ids
		}

		if !reflect.DeepEqual(httpMon.Base.NotificationIDs, targetIDs) {
			httpMon.Base.NotificationIDs = targetIDs
			updated = true
		}

		if updated {
			if err := client.UpdateMonitor(ctx, &httpMon); err != nil {
				return fmt.Errorf("failed to update http monitor %d: %w", monID, err)
			}
		}

	default:
		log.Printf("Skipping update for monitor type %s (not supported yet)", mcfg.Type)
		return nil
	}

	if updated {
		log.Printf("Updated monitor %s (description/notifications)", mcfg.Name)
	}

	return nil
}

func provisionKumaMonitor(ctx context.Context, client *kuma.Client, cfg *Config) error {
	log.Println("Starting provisioning...")

	monitors, err := client.GetMonitors(ctx)
	if err != nil {
		return fmt.Errorf("failed to get monitors: %w", err)
	}

	existingByName := make(map[string]monitor.Base)
	for _, m := range monitors {
		existingByName[m.Name] = m
	}
	log.Printf("Found %d existing monitors", len(existingByName))

	groupNotificationIDs := []int64{}
	if len(cfg.GroupNotificationNames) > 0 {
		ids, err := resolveNotificationIDs(ctx, client, cfg.GroupNotificationNames)
		if err != nil {
			return err
		}
		groupNotificationIDs = ids
	}

	// Create or update group
	var groupID int64
	if groupMon, exists := existingByName[cfg.GroupName]; exists {
		groupID = groupMon.GetID()
		log.Printf("Group exists: %s (ID: %d)", cfg.GroupName, groupID)

		// === UPDATE EXISTING GROUP ===
		var currentGroup monitor.Group
		if err := client.GetMonitorAs(ctx, groupID, &currentGroup); err != nil {
			log.Printf("Warning: failed to fetch group %s for update: %v", cfg.GroupName, err)
		} else {
			updated := false

			// Update description if different
			if (currentGroup.Base.Description == nil && cfg.GroupDescription != nil) ||
				(currentGroup.Base.Description != nil && cfg.GroupDescription != nil && *currentGroup.Base.Description != *cfg.GroupDescription) ||
				(currentGroup.Base.Description != nil && cfg.GroupDescription == nil) {
				currentGroup.Base.Description = cfg.GroupDescription
				updated = true
			}

			// Update notifications if different
			if !reflect.DeepEqual(currentGroup.Base.NotificationIDs, groupNotificationIDs) {
				currentGroup.Base.NotificationIDs = groupNotificationIDs
				updated = true
			}

			if updated {
				if err := client.UpdateMonitor(ctx, &currentGroup); err != nil {
					log.Printf("Warning: failed to update group %s: %v", cfg.GroupName, err)
				} else {
					log.Printf("Updated group %s (description/notifications)", cfg.GroupName)
				}
			}
		}
	} else {
		// === CREATE NEW GROUP ===
		group := &monitor.Group{
			Base: monitor.Base{
				Name:            cfg.GroupName,
				Description:     cfg.GroupDescription,
				NotificationIDs: groupNotificationIDs,
				Interval:        int64(cfg.Interval),
				MaxRetries:      int64(cfg.MaxRetries),
				IsActive:        true,
			},
		}
		id, err := client.CreateMonitor(ctx, group)
		if err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		groupID = id
		log.Printf("Created group: %s (ID: %d)", cfg.GroupName, groupID)
	}

	parent := &groupID

	// Track if config was updated with new tokens
	configUpdated := false

	for i := range cfg.Monitors {
		mcfg := &cfg.Monitors[i]
		if existing, exists := existingByName[mcfg.Name]; exists {
			log.Printf("Monitor exists: %s (ID: %d)", mcfg.Name, existing.GetID())

			// Always try to fetch the push token for push monitors
			if mcfg.Type == "push" {
				var push monitor.Push
				if err := client.GetMonitorAs(ctx, existing.GetID(), &push); err == nil {
					if push.PushDetails.PushToken != "" && mcfg.PushToken != push.PushDetails.PushToken {
						mcfg.PushToken = push.PushDetails.PushToken
						configUpdated = true
						log.Printf("Fetched and updated push token for existing monitor %s", mcfg.Name)
					}
				} else {
					log.Printf("Failed to fetch token for existing monitor %s: %v", mcfg.Name, err)
				}
			}

			// Resolve target notifications
			targetIDs := []int64{}
			if len(mcfg.NotificationNames) > 0 {
				ids, err := resolveNotificationIDs(ctx, client, mcfg.NotificationNames)
				if err != nil {
					log.Printf("Warning: failed to resolve notifications for %s: %v", mcfg.Name, err)
				} else {
					targetIDs = ids
				}
			}

			// Update description + notifications (works for ALL types)
			if err := updateMonitorBase(ctx, client, existing.GetID(), mcfg, targetIDs); err != nil {
				log.Printf("Warning: failed to update monitor %s: %v", mcfg.Name, err)
			}

			continue // skip creation
		}

		notificationIDs := []int64{}

		// Monitor-specific override
		if len(mcfg.NotificationNames) > 0 {
			ids, err := resolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			notificationIDs = ids
		}

		// New monitor creation
		base := monitor.Base{
			Name:            mcfg.Name,
			Description:     mcfg.Description,
			NotificationIDs: notificationIDs,
			Interval:        int64(cfg.Interval),
			MaxRetries:      int64(cfg.MaxRetries),
			IsActive:        true,
			Parent:          parent,
		}

		var mon monitor.Monitor
		switch mcfg.Type {
		case "push":
			// Generate unique token in code
			customToken, err := generatePushToken()
			if err != nil {
				return fmt.Errorf("failed to generate push token: %w", err)
			}
			log.Printf("Generated custom push token for '%s': %s", mcfg.Name, customToken)

			displayName := mcfg.Name
			base.Name = displayName
			pushMon := &monitor.Push{
				Base: base,
				PushDetails: monitor.PushDetails{
					PushToken: customToken, // This is the key line!
				},
			}
			mon = pushMon
		case "http":
			if mcfg.URL == "" {
				return fmt.Errorf("http monitor %s missing url", mcfg.Name)
			}
			mon = &monitor.HTTP{
				Base: base,
				HTTPDetails: monitor.HTTPDetails{
					URL:                 mcfg.URL,
					Method:              "GET",
					Body:                "",
					HTTPBodyEncoding:    "text",
					Headers:             "{}",
					AcceptedStatusCodes: []string{"200-299"},
					MaxRedirects:        10,
					Timeout:             30,
				},
			}
		default:
			return fmt.Errorf("unsupported type: %s", mcfg.Type)
		}

		id, err := client.CreateMonitor(ctx, mon)
		if err != nil {
			return fmt.Errorf("create %s monitor %s: %w", mcfg.Type, mcfg.Name, err)
		}

		// Fetch token for newly created push monitor
		if mcfg.Type == "push" {
			var push monitor.Push
			if err := client.GetMonitorAs(ctx, id, &push); err == nil {
				if push.PushDetails.PushToken != "" {
					mcfg.PushToken = push.PushDetails.PushToken
					configUpdated = true
					log.Printf("Fetched push token for new monitor %s: %s", mcfg.Name, mcfg.PushToken)
				} else {
					log.Printf("New push monitor %s created but token empty", mcfg.Name)
				}
			} else {
				log.Printf("Failed to fetch token for new monitor %s: %v", mcfg.Name, err)
			}
		}

		log.Printf("Created %s monitor: %s (ID: %d)", mcfg.Type, mcfg.Name, id)
	}

	// Always save config if tokens were updated
	if configUpdated {
		if err := saveConfig(cfg); err != nil {
			log.Printf("Warning: failed to save updated config with tokens: %v", err)
		} else {
			log.Println("Saved updated config with push tokens")
		}
	}

	if withTelegraf {
		log.Printf("withTelegraf flag: %t - generating configs", withTelegraf)
		if err := generateTelegrafConfigs(cfg); err != nil {
			return err
		}
	}

	return nil
}

func generateTelegrafConfigs(cfg *Config) error {
	log.Println("Starting Telegraf drop-in generation...")

	if err := os.MkdirAll(telegrafDir, 0755); err != nil {
		return fmt.Errorf("failed to create telegraf directory %s: %w", telegrafDir, err)
	}

	// === Clean up old generated input files (05-inputs-*.conf) ===
	entries, err := os.ReadDir(telegrafDir)
	if err != nil {
		return fmt.Errorf("failed to read telegraf dir: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "05-inputs-") && strings.HasSuffix(name, ".conf") {
			if err := os.Remove(filepath.Join(telegrafDir, name)); err != nil {
				log.Printf("Warning: failed to remove old input file %s: %v", name, err)
			} else {
				log.Printf("Removed old input config: %s", name)
			}
		}
	}

	// === Determine needed metric types and collect disk mount points ===
	type metricInfo struct {
		Field         string
		Threshold     float64
		Token         string
		Name          string
		Filesystem    string // only for disk
		ContainerName string // only for docker
	}

	neededMetrics := make(map[string]bool)           // "cpu", "mem", "disk"
	monitorByMetric := make(map[string][]metricInfo) // for validation and outputs

	var diskMountPoints []string
	diskSeen := make(map[string]bool)

	for i := range cfg.Monitors {
		m := &cfg.Monitors[i]
		if m.Type != "push" || m.Metric == "" || m.PushToken == "" {
			continue
		}

		m.resolveMetrics() // your existing logic to set defaults

		if m.Threshold == 0 {
			m.Threshold = 90.0
		}

		neededMetrics[m.Metric] = true

		info := metricInfo{
			Field:         m.Field,
			Threshold:     m.Threshold,
			Token:         m.PushToken,
			Name:          m.Name,
			Filesystem:    m.Filesystem,
			ContainerName: m.ContainerName,
		}
		monitorByMetric[m.Metric] = append(monitorByMetric[m.Metric], info)

		if m.Metric == "disk" && m.Filesystem != "" {
			fs := strings.TrimSpace(m.Filesystem)
			if !diskSeen[fs] {
				diskSeen[fs] = true
				diskMountPoints = append(diskMountPoints, fs)
			}
		}
	}

	// === Helper: render embedded template to file ===
	renderTemplate := func(templatePath, outputPath string, data any) error {
		content, err := templateFS.ReadFile(templatePath)
		if err != nil {
			return fmt.Errorf("failed to read embedded template %s: %w", templatePath, err)
		}

		tmpl, err := template.New(filepath.Base(templatePath)).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", templatePath, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute template %s: %w", templatePath, err)
		}

		output := buf.String()
		if !strings.HasSuffix(output, "\n") {
			output += "\n"
		}

		if err := os.WriteFile(outputPath, []byte(output), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}

		log.Printf("Generated: %s", outputPath)
		return nil
	}

	// === 1. Generate input configs only if needed ===

	if neededMetrics["cpu"] {
		if err := renderTemplate("templates/inputs_cpu.tmpl",
			filepath.Join(telegrafDir, "05-inputs-cpu.conf"), nil); err != nil {
			return err
		}
	}

	if neededMetrics["mem"] {
		if err := renderTemplate("templates/inputs_mem.tmpl",
			filepath.Join(telegrafDir, "05-inputs-mem.conf"), nil); err != nil {
			return err
		}
	}

	// Generate single docker input only if needed
	hasDockerMetric := false
	for metric := range neededMetrics {
		if strings.Contains(strings.ToLower(metric), "docker") {
			hasDockerMetric = true
			break
		}
	}

	if hasDockerMetric {
		if err := renderTemplate("templates/inputs_docker.tmpl",
			filepath.Join(telegrafDir, "05-inputs-docker.conf"), nil); err != nil {
			return err
		}
	}

	if len(diskMountPoints) > 0 {
		sort.Strings(diskMountPoints) // deterministic
		if err := renderTemplate("templates/inputs_disk.tmpl",
			filepath.Join(telegrafDir, "05-inputs-disk.conf"),
			struct{ MountPoints []string }{MountPoints: diskMountPoints},
		); err != nil {
			return err
		}
	}

	// === 2. Generate global outputs.discard if configured ===
	useOutputsDiscard := true
	if cfg.Agent.UseOutputsDiscard != nil {
		useOutputsDiscard = *cfg.Agent.UseOutputsDiscard
	}

	if useOutputsDiscard {
		// Assuming you have this template embedded
		if err := renderTemplate("templates/outputs_discard.tmpl",
			filepath.Join(telegrafDir, "00-outputs-discard.conf"), nil); err != nil {
			return err
		}
	}

	// === 3. Generate one outputs.exec per push monitor ===
	pushCount := 0
	for metric, monitors := range monitorByMetric {
		for _, m := range monitors {
			pushCount++

			safeName := sanitizeFilename(m.Name)
			filename := fmt.Sprintf("90-uptime-kuma-push-%s.conf", safeName)
			path := filepath.Join(telegrafDir, filename)

			data := struct {
				DockerImage   string
				MonitorName   string
				Token         string
				Metric        string
				Field         string
				Threshold     float64
				ContainerName string
				Filesystem    string
			}{
				DockerImage:   cfg.Agent.DockerImage,
				MonitorName:   m.Name,
				Token:         m.Token,
				Metric:        metric,
				Field:         m.Field,
				Threshold:     m.Threshold,
				ContainerName: m.ContainerName,
				Filesystem:    m.Filesystem,
			}

			// You'll need this template too: templates/outputs_exec_push.tmpl
			if err := renderTemplate("templates/outputs_exec_push.tmpl", path, data); err != nil {
				return err
			}
		}
	}

	log.Printf("Telegraf generation complete: %d push monitor(s), inputs: cpu=%v mem=%v disk=%v, discard=%v",
		pushCount,
		neededMetrics["cpu"], neededMetrics["mem"], len(diskMountPoints) > 0,
		useOutputsDiscard)

	return nil
}

var pushMetricCmd = &cobra.Command{
	Use:   "push-metric",
	Short: "One-shot push triggered by Telegraf outputs.exec",
	Run: func(cmd *cobra.Command, args []string) {
		monitorName := cmd.Flag("monitor").Value.String()
		token := cmd.Flag("token").Value.String()

		if monitorName == "" || token == "" {
			log.Fatalf("Missing required flags: monitor=%q token=%q", monitorName, token)
		}

		log.Printf("=== push-metric STARTED (outputs.exec mode) ===")
		log.Printf("Monitor: %s", monitorName)
		log.Printf("Token: %s", token)
		log.Printf("Config path: %s", configPath)

		// Load full config
		cfg, err := loadMergedConfig(filepath.Dir(configPath))
		if err != nil {
			log.Fatalf("Failed to load merged config: %v", err)
		}

		pushURL := fmt.Sprintf("%s/api/push/%s", strings.TrimSuffix(cfg.UptimeKumaURL, "/"), token)
		log.Printf("Push URL: %s", pushURL)

		// Find threshold and field from config.yaml (single lookup)
		threshold := 90.0
		expectedField := ""

		for _, m := range cfg.Monitors {
			if m.Type == "push" && m.Name == monitorName {
				if m.Threshold > 0 {
					threshold = m.Threshold
				}
				if m.Field != "" {
					expectedField = m.Field
				}
				break
			}
		}

		// Enforce that field is defined
		if expectedField == "" {
			log.Fatalf("CRITICAL: No 'field' defined for monitor %q in config.yaml", monitorName)
		}

		log.Printf("Threshold from config.yaml: %.1f", threshold)
		log.Printf("Expecting field: %s", expectedField)
		// READ ALL FROM STDIN
		var value float64
		found := false
		lineCount := 0
		var receivedLines []string // for debug on failure

		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			lineCount++
			receivedLines = append(receivedLines, line)
			log.Printf("STDIN line %d: %s", lineCount, line)

			// Robust parsing: find field even if surrounded by tags or other fields
			if strings.Contains(line, expectedField+"=") {
				// Find the start of the value
				idx := strings.Index(line, expectedField+"=")
				rest := line[idx+len(expectedField)+1:]

				// Extract value until comma or end
				valStr := strings.SplitN(rest, ",", 2)[0]
				valStr = strings.SplitN(valStr, " ", 2)[0]
				valStr = strings.TrimSpace(valStr)

				// Remove any trailing unit suffix (like 'u') if present
				if len(valStr) > 0 && valStr[len(valStr)-1] == 'u' {
					valStr = valStr[:len(valStr)-1]
				}

				if v, err := strconv.ParseFloat(valStr, 64); err == nil {
					value = v
					found = true
					log.Printf("PARSED %.6f from field '%s' (raw value: %q)", value, expectedField, valStr)
				} else {
					log.Printf("PARSE FAILED for field '%s': raw value %q → error: %v", expectedField, valStr, err)
				}
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("Error reading STDIN: %v", err)
			os.Exit(1)
		}

		log.Printf("Total lines read from STDIN: %d | Found matching field: %v", lineCount, found)

		if lineCount == 0 {
			log.Printf("CRITICAL: NO DATA RECEIVED ON STDIN — Telegraf sent nothing!")
			os.Exit(1)
		}

		if !found {
			log.Printf("FAILED: Expected field '%s=' not found in any line", expectedField)
			log.Printf("Received %d line(s):", lineCount)
			for i, l := range receivedLines {
				log.Printf("  Line %d: %s", i+1, l)
			}
			os.Exit(1)
		}

		// Determine status
		status := "up"
		if value > threshold {
			status = "down"
		}

		// Build message and URL
		msg := fmt.Sprintf("%s: %.2f%% (threshold %.0f%%)", monitorName, value, threshold)
		fullURL := fmt.Sprintf("%s?status=%s&ping=%.2f&msg=%s", pushURL, status, value, url.QueryEscape(msg))
		log.Printf("Final push URL: %s", fullURL)

		// Perform HTTP push
		resp, err := http.Get(fullURL)
		if err != nil {
			log.Printf("HTTP request failed: %v", err)
			os.Exit(1)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			log.Printf("Push failed: %d %s", resp.StatusCode, string(body))
			os.Exit(1)
		}

		log.Printf("PUSH SUCCESS: %s → %.1f%% (%s)", monitorName, value, status)
		os.Exit(0)
	},
}
