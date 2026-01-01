package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	kuma "github.com/breml/go-uptime-kuma-client"
	"github.com/breml/go-uptime-kuma-client/monitor"
	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/gitisz/uptime-kuma-agent/internal/logging"
)

var invalidChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var multipleHyphens = regexp.MustCompile(`-+`) // Match one or more hyphens

func GeneratePushToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func SanitizeFilename(name string) string {
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
func ResolveNotificationIDs(ctx context.Context, client *kuma.Client, names []string) ([]int64, error) {
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
		logging.Warnf("Warning: notification names not found: %v", missing)
	}

	return ids, nil
}

func UpdateMonitorBase(ctx context.Context, client *kuma.Client, monID int64, mcfg *config.MonitorConfig, groupNotificationIDs []int64) error {
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
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
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
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
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
		logging.Warnf("Skipping update for monitor type %s (not supported yet)", mcfg.Type)
		return nil
	}

	if updated {
		logging.Infof("Updated monitor %s (description/notifications)", mcfg.Name)
	}

	return nil
}

func ProvisionKumaMonitor(ctx context.Context, client *kuma.Client, cfg *config.Config) error {
	logging.Info("Starting provisioning...")

	monitors, err := client.GetMonitors(ctx)
	if err != nil {
		return fmt.Errorf("failed to get monitors: %w", err)
	}

	// Build lookup maps for existing monitors
	existingByName := make(map[string]monitor.Base)         // For monitors without groups
	existingByNameAndGroup := make(map[string]monitor.Base) // For monitors with groups: "name|groupID"

	for _, m := range monitors {
		existingByName[m.Name] = m

		// Also index by name + group for grouped monitors
		groupKey := ""
		if m.Parent != nil {
			groupKey = fmt.Sprintf("%s|%d", m.Name, *m.Parent)
		} else {
			groupKey = fmt.Sprintf("%s|", m.Name) // Empty group
		}
		existingByNameAndGroup[groupKey] = m
	}
	logging.Infof("Found %d existing monitors", len(existingByName))

	// Create/update all groups and build groupName -> ID map
	groupNameToID := make(map[string]int64)
	for _, gcfg := range cfg.Groups {
		// Resolve group notification IDs
		groupNotificationIDs := []int64{}
		if len(gcfg.NotificationNames) > 0 {
			ids, err := ResolveNotificationIDs(ctx, client, gcfg.NotificationNames)
			if err != nil {
				return fmt.Errorf("resolve notifications for group %s: %w", gcfg.Name, err)
			}
			groupNotificationIDs = ids
		}

		// Check if group exists
		if groupMon, exists := existingByName[gcfg.Name]; exists {
			groupID := groupMon.GetID()
			groupNameToID[gcfg.Name] = groupID
			logging.Infof("Group exists: %s (ID: %d)", gcfg.Name, groupID)

			// Update existing group
			var currentGroup monitor.Group
			if err := client.GetMonitorAs(ctx, groupID, &currentGroup); err == nil {
				updated := false
				if (currentGroup.Base.Description == nil && gcfg.Description != nil) ||
					(currentGroup.Base.Description != nil && gcfg.Description != nil && *currentGroup.Base.Description != *gcfg.Description) ||
					(currentGroup.Base.Description != nil && gcfg.Description == nil) {
					currentGroup.Base.Description = gcfg.Description
					updated = true
				}
				if !reflect.DeepEqual(currentGroup.Base.NotificationIDs, groupNotificationIDs) {
					currentGroup.Base.NotificationIDs = groupNotificationIDs
					updated = true
				}
				if updated {
					if err := client.UpdateMonitor(ctx, &currentGroup); err != nil {
						logging.Warnf("Warning: failed to update group %s: %v", gcfg.Name, err)
					} else {
						logging.Infof("Updated group %s", gcfg.Name)
					}
				}
			}
		} else {
			// Create new group
			group := &monitor.Group{
				Base: monitor.Base{
					Name:            gcfg.Name,
					Description:     gcfg.Description,
					NotificationIDs: groupNotificationIDs,
					Interval:        int64(cfg.Interval),
					MaxRetries:      int64(cfg.MaxRetries),
					IsActive:        true,
				},
			}
			id, err := client.CreateMonitor(ctx, group)
			if err != nil {
				return fmt.Errorf("create group %s: %w", gcfg.Name, err)
			}
			groupNameToID[gcfg.Name] = id
			logging.Infof("Created group: %s (ID: %d)", gcfg.Name, id)
		}
	}

	// Track if config was updated with new tokens
	configUpdated := false

	// Process push monitors first to update tokens
	for i := range cfg.PushMonitors {
		mcfg := &cfg.PushMonitors[i]
		mcfg.Type = "push" // Ensure type is set
		mcfg.ResolveMetrics(cfg)

		// Check if this monitor exists
		var existing monitor.Base
		var exists bool

		if mcfg.Group != "" {
			// Monitor has a group - lookup by name + group ID
			if groupID, groupExists := groupNameToID[mcfg.Group]; groupExists {
				groupKey := fmt.Sprintf("%s|%d", mcfg.Name, groupID)
				existing, exists = existingByNameAndGroup[groupKey]
				if exists {
					logging.Infof("Grouped push monitor exists: %s (group: %s, ID: %d)", mcfg.Name, mcfg.Group, existing.GetID())
				}
			} else {
				logging.Warnf("Push monitor %s specifies unknown group %q - treating as ungrouped", mcfg.Name, mcfg.Group)
				// Fall back to name-only lookup for unknown groups
				existing, exists = existingByName[mcfg.Name]
			}
		} else {
			// Monitor has no group - lookup by name only (can be overwritten)
			existing, exists = existingByName[mcfg.Name]
			if exists {
				logging.Infof("Ungrouped push monitor exists: %s (ID: %d) - will be updated/overwritten", mcfg.Name, existing.GetID())
			}
		}

		if exists {
			// Fetch the push token for existing monitors
			var push monitor.Push
			if err := client.GetMonitorAs(ctx, existing.GetID(), &push); err == nil {
				if push.PushDetails.PushToken != "" && mcfg.PushToken != push.PushDetails.PushToken {
					mcfg.PushToken = push.PushDetails.PushToken
					configUpdated = true
					logging.Infof("Fetched and updated push token for existing monitor %s", mcfg.Name)
				}
			} else {
				logging.Errorf("Failed to fetch token for existing monitor %s: %v", mcfg.Name, err)
			}

			// Resolve target notifications
			targetIDs := []int64{}
			if len(mcfg.NotificationNames) > 0 {
				ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
				if err != nil {
					logging.Warnf("Warning: failed to resolve notifications for %s: %v", mcfg.Name, err)
				} else {
					targetIDs = ids
				}
			}

			// Update description + notifications
			if err := UpdateMonitorBase(ctx, client, existing.GetID(), mcfg, targetIDs); err != nil {
				logging.Warnf("Warning: failed to update monitor %s: %v", mcfg.Name, err)
			}

			continue // skip creation
		}

		// Create new push monitor
		notificationIDs := []int64{}
		if len(mcfg.NotificationNames) > 0 {
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			notificationIDs = ids
		}

		// Determine parent group ID
		var parent *int64
		if mcfg.Group != "" {
			if groupID, exists := groupNameToID[mcfg.Group]; exists {
				parent = &groupID
			} else {
				logging.Warnf("Push monitor %s specifies unknown group %q", mcfg.Name, mcfg.Group)
			}
		} else if len(cfg.Groups) > 0 {
			// Default to first group if no group specified
			if groupID, exists := groupNameToID[cfg.Groups[0].Name]; exists {
				parent = &groupID
				logging.Debugf("Push monitor %s defaults to first group %q (ID: %d)", mcfg.Name, cfg.Groups[0].Name, *parent)
			}
		}

		// Generate unique token
		customToken, err := GeneratePushToken()
		if err != nil {
			return fmt.Errorf("failed to generate push token: %w", err)
		}
		logging.Debugf("Generated custom push token for '%s': %s", mcfg.Name, customToken)

		pushMon := &monitor.Push{
			Base: monitor.Base{
				Name:            mcfg.Name,
				Description:     mcfg.Description,
				NotificationIDs: notificationIDs,
				Interval:        int64(cfg.Interval),
				MaxRetries:      int64(cfg.MaxRetries),
				IsActive:        true,
				Parent:          parent,
			},
			PushDetails: monitor.PushDetails{
				PushToken: customToken,
			},
		}

		id, err := client.CreateMonitor(ctx, pushMon)
		if err != nil {
			return fmt.Errorf("create push monitor %s: %w", mcfg.Name, err)
		}

		// Fetch the actual token from the created monitor
		if err := client.GetMonitorAs(ctx, id, &pushMon); err == nil {
			if pushMon.PushDetails.PushToken != "" {
				mcfg.PushToken = pushMon.PushDetails.PushToken
				configUpdated = true
				logging.Debugf("Fetched push token for new monitor %s: %s", mcfg.Name, mcfg.PushToken)
			} else {
				logging.Warnf("New push monitor %s created but token empty", mcfg.Name)
			}
		} else {
			logging.Errorf("Failed to fetch token for new monitor %s: %v", mcfg.Name, err)
		}

		logging.Infof("Created push monitor: %s (ID: %d)", mcfg.Name, id)
	}

	// Process HTTP monitors
	for i := range cfg.HTTPMonitors {
		mcfg := &cfg.HTTPMonitors[i]
		mcfg.Type = "http" // Ensure type is set
		mcfg.ResolveMetrics(cfg)

		// Check if this monitor exists
		var existing monitor.Base
		var exists bool

		if mcfg.Group != "" {
			// Monitor has a group - lookup by name + group ID
			if groupID, groupExists := groupNameToID[mcfg.Group]; groupExists {
				groupKey := fmt.Sprintf("%s|%d", mcfg.Name, groupID)
				existing, exists = existingByNameAndGroup[groupKey]
				if exists {
					logging.Infof("Grouped HTTP monitor exists: %s (group: %s, ID: %d)", mcfg.Name, mcfg.Group, existing.GetID())
				}
			} else {
				logging.Warnf("HTTP monitor %s specifies unknown group %q - treating as ungrouped", mcfg.Name, mcfg.Group)
				// Fall back to name-only lookup for unknown groups
				existing, exists = existingByName[mcfg.Name]
			}
		} else {
			// Monitor has no group - lookup by name only (can be overwritten)
			existing, exists = existingByName[mcfg.Name]
			if exists {
				logging.Infof("Ungrouped HTTP monitor exists: %s (ID: %d) - will be updated/overwritten", mcfg.Name, existing.GetID())
			}
		}

		if exists {
			// Resolve target notifications
			targetIDs := []int64{}
			if len(mcfg.NotificationNames) > 0 {
				ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
				if err != nil {
					logging.Warnf("Warning: failed to resolve notifications for %s: %v", mcfg.Name, err)
				} else {
					targetIDs = ids
				}
			}

			// Update description + notifications
			if err := UpdateMonitorBase(ctx, client, existing.GetID(), mcfg, targetIDs); err != nil {
				logging.Warnf("Warning: failed to update monitor %s: %v", mcfg.Name, err)
			}

			continue // skip creation
		}

		// Create new HTTP monitor
		if mcfg.URL == "" {
			return fmt.Errorf("http monitor %s missing url", mcfg.Name)
		}

		notificationIDs := []int64{}
		if len(mcfg.NotificationNames) > 0 {
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			notificationIDs = ids
		}

		// Determine parent group ID
		var parent *int64
		if mcfg.Group != "" {
			if groupID, exists := groupNameToID[mcfg.Group]; exists {
				parent = &groupID
			} else {
				logging.Warnf("HTTP monitor %s specifies unknown group %q", mcfg.Name, mcfg.Group)
			}
		} else if len(cfg.Groups) > 0 {
			// Default to first group if no group specified
			if groupID, exists := groupNameToID[cfg.Groups[0].Name]; exists {
				parent = &groupID
				logging.Debugf("HTTP monitor %s defaults to first group %q (ID: %d)", mcfg.Name, cfg.Groups[0].Name, *parent)
			}
		}

		httpMon := &monitor.HTTP{
			Base: monitor.Base{
				Name:            mcfg.Name,
				Description:     mcfg.Description,
				NotificationIDs: notificationIDs,
				Interval:        int64(cfg.Interval),
				MaxRetries:      int64(cfg.MaxRetries),
				IsActive:        true,
				Parent:          parent,
			},
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

		id, err := client.CreateMonitor(ctx, httpMon)
		if err != nil {
			return fmt.Errorf("create http monitor %s: %w", mcfg.Name, err)
		}

		logging.Infof("Created HTTP monitor: %s (ID: %d)", mcfg.Name, id)
	}

	// Process legacy monitors (for backward compatibility)
	for i := range cfg.Monitors {
		mcfg := &cfg.Monitors[i]
		mcfg.ResolveMetrics(cfg)

		// Check if this monitor exists (legacy monitors don't have groups)
		existing, exists := existingByName[mcfg.Name]
		if exists {
			logging.Infof("Legacy monitor exists: %s (ID: %d) - will be updated/overwritten", mcfg.Name, existing.GetID())

			// Resolve target notifications
			targetIDs := []int64{}
			if len(mcfg.NotificationNames) > 0 {
				ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
				if err != nil {
					logging.Warnf("Warning: failed to resolve notifications for %s: %v", mcfg.Name, err)
				} else {
					targetIDs = ids
				}
			}

			// Update description + notifications
			if err := UpdateMonitorBase(ctx, client, existing.GetID(), mcfg, targetIDs); err != nil {
				logging.Warnf("Warning: failed to update monitor %s: %v", mcfg.Name, err)
			}

			continue // skip creation
		}

		// Create new legacy monitor
		if mcfg.URL == "" && mcfg.Type == "http" {
			return fmt.Errorf("legacy http monitor %s missing url", mcfg.Name)
		}

		notificationIDs := []int64{}
		if len(mcfg.NotificationNames) > 0 {
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
			if err != nil {
				return err
			}
			notificationIDs = ids
		}

		// Legacy monitors default to first group
		var parent *int64
		if len(cfg.Groups) > 0 {
			if groupID, exists := groupNameToID[cfg.Groups[0].Name]; exists {
				parent = &groupID
				logging.Debugf("Legacy monitor %s defaults to first group %q (ID: %d)", mcfg.Name, cfg.Groups[0].Name, *parent)
			}
		}

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
			customToken, err := GeneratePushToken()
			if err != nil {
				return fmt.Errorf("failed to generate push token: %w", err)
			}
			logging.Debugf("Generated custom push token for legacy '%s': %s", mcfg.Name, customToken)

			pushMon := &monitor.Push{
				Base: base,
				PushDetails: monitor.PushDetails{
					PushToken: customToken,
				},
			}
			mon = pushMon
		case "http":
			httpMon := &monitor.HTTP{
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
			mon = httpMon
		default:
			return fmt.Errorf("unsupported legacy type: %s", mcfg.Type)
		}

		id, err := client.CreateMonitor(ctx, mon)
		if err != nil {
			return fmt.Errorf("create legacy %s monitor %s: %w", mcfg.Type, mcfg.Name, err)
		}

		// Fetch token for newly created push monitor
		if mcfg.Type == "push" {
			var push monitor.Push
			if err := client.GetMonitorAs(ctx, id, &push); err == nil {
				if push.PushDetails.PushToken != "" {
					mcfg.PushToken = push.PushDetails.PushToken
					configUpdated = true
					logging.Debugf("Fetched push token for legacy monitor %s: %s", mcfg.Name, mcfg.PushToken)
				} else {
					logging.Warnf("New legacy push monitor %s created but token empty", mcfg.Name)
				}
			} else {
				logging.Errorf("Failed to fetch token for legacy monitor %s: %v", mcfg.Name, err)
			}
		}

		logging.Infof("Created legacy %s monitor: %s (ID: %d)", mcfg.Type, mcfg.Name, id)
	}

	// Always save config if tokens were updated
	if configUpdated {
		if err := config.SaveConfig("/config/config.yaml", cfg); err != nil {
			logging.Warnf("Warning: failed to save updated config with tokens: %v", err)
		} else {
			logging.Info("Saved updated config with push tokens")
		}
	}

	return nil
}
