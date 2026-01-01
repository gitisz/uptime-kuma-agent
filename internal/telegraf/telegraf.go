package telegraf

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/gitisz/uptime-kuma-agent/internal/logging"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

func GenerateTelegrafConfigs(cfg *config.Config, telegrafDir string) error {
	logging.Info("Starting Telegraf drop-in generation...")

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
				logging.Warnf("Warning: failed to remove old input file %s: %v", name, err)
			} else {
				logging.Infof("Removed old input config: %s", name)
			}
		}
	}

	// === Determine needed metric types and collect disk mount points ===
	type metricInfo struct {
		Field         string
		Threshold     float64
		Token         string
		Name          string
		Group         string
		Filesystem    string // only for disk
		ContainerName string // only for docker
	}

	neededMetrics := make(map[string]bool)           // "cpu", "mem", "disk"
	monitorByMetric := make(map[string][]metricInfo) // for validation and outputs

	var diskMountPoints []string
	diskSeen := make(map[string]bool)

	allMonitors := cfg.GetAllMonitors()
	for i := range allMonitors {
		m := &allMonitors[i]
		if m.Type != "push" || m.Metric == "" || m.PushToken == "" {
			continue
		}

		m.ResolveMetrics(cfg) // use global thresholds for defaults

		neededMetrics[m.Metric] = true

		info := metricInfo{
			Field:         m.Field,
			Threshold:     m.Threshold,
			Token:         m.PushToken,
			Name:          m.Name,
			Group:         m.Group,
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

		logging.Infof("Generated: %s", outputPath)
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

			safeName := strings.ToLower(strings.ReplaceAll(m.Name, " ", "-"))
			filename := fmt.Sprintf("90-uptime-kuma-push-%s.conf", safeName)
			path := filepath.Join(telegrafDir, filename)

			// Determine log directories from logging config
			hostLogDirectory := logging.GetHostLogDirectory(&cfg.Agent.Logging)
			internalLogDirectory := logging.GetInternalLogDirectory(&cfg.Agent.Logging)

			data := struct {
				DockerImage          string
				MonitorName          string
				Group                string
				Token                string
				Metric               string
				Field                string
				Threshold            float64
				ContainerName        string
				Filesystem           string
				HostLogDirectory     string
				InternalLogDirectory string
			}{
				DockerImage:          cfg.Agent.DockerImage,
				MonitorName:          m.Name,
				Group:                m.Group,
				Token:                m.Token,
				Metric:               metric,
				Field:                m.Field,
				Threshold:            m.Threshold,
				ContainerName:        m.ContainerName,
				Filesystem:           m.Filesystem,
				HostLogDirectory:     hostLogDirectory,
				InternalLogDirectory: internalLogDirectory,
			}

			// You'll need this template too: templates/outputs_exec_push.tmpl
			if err := renderTemplate("templates/outputs_exec_push.tmpl", path, data); err != nil {
				return err
			}
		}
	}

	logging.Infof("Telegraf generation complete: %d push monitor(s), inputs: cpu=%v mem=%v disk=%v, discard=%v",
		pushCount,
		neededMetrics["cpu"], neededMetrics["mem"], len(diskMountPoints) > 0,
		useOutputsDiscard)

	return nil
}
