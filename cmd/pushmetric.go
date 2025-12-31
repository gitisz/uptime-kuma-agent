package cmd

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/spf13/cobra"
)

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
		cfg, err := config.LoadMergedConfig(filepath.Dir(configPath))
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
