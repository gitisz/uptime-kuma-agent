package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"regexp"
	"strings"

	kuma "github.com/breml/go-uptime-kuma-client"
	"github.com/breml/go-uptime-kuma-client/monitor"
	"github.com/gitisz/uptime-kuma-agent/internal/config"
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
		log.Printf("Warning: notification names not found: %v", missing)
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
		log.Printf("Skipping update for monitor type %s (not supported yet)", mcfg.Type)
		return nil
	}

	if updated {
		log.Printf("Updated monitor %s (description/notifications)", mcfg.Name)
	}

	return nil
}

func ProvisionKumaMonitor(ctx context.Context, client *kuma.Client, cfg *config.Config) error {
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
		ids, err := ResolveNotificationIDs(ctx, client, cfg.GroupNotificationNames)
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
		if existing, exists := existingByName[mcfg.Name]; exists && existing.Parent != nil && *existing.Parent == groupID {
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
				ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
				if err != nil {
					log.Printf("Warning: failed to resolve notifications for %s: %v", mcfg.Name, err)
				} else {
					targetIDs = ids
				}
			}

			// Update description + notifications (works for ALL types)
			if err := UpdateMonitorBase(ctx, client, existing.GetID(), mcfg, targetIDs); err != nil {
				log.Printf("Warning: failed to update monitor %s: %v", mcfg.Name, err)
			}

			continue // skip creation
		}

		notificationIDs := []int64{}

		// Monitor-specific override
		if len(mcfg.NotificationNames) > 0 {
			ids, err := ResolveNotificationIDs(ctx, client, mcfg.NotificationNames)
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
			customToken, err := GeneratePushToken()
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
		if err := config.SaveConfig("/config/config.yaml", cfg); err != nil {
			log.Printf("Warning: failed to save updated config with tokens: %v", err)
		} else {
			log.Println("Saved updated config with push tokens")
		}
	}

	return nil
}
