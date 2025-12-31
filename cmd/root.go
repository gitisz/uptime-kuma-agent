package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	kuma "github.com/breml/go-uptime-kuma-client"
	"github.com/gitisz/uptime-kuma-agent/internal/config"
	"github.com/gitisz/uptime-kuma-agent/internal/provision"
	"github.com/gitisz/uptime-kuma-agent/internal/telegraf"
	"github.com/spf13/cobra"
)

var (
	configPath   string
	telegrafDir  = "/etc/telegraf/telegraf.d"
	withTelegraf bool
)

func NewRootCmd() *cobra.Command {
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

	// Add push-metric subcommand
	rootCmd.AddCommand(pushMetricCmd)
	pushMetricCmd.Flags().String("monitor", "", "Monitor name")
	pushMetricCmd.Flags().String("token", "", "Push token")
	pushMetricCmd.MarkFlagRequired("monitor")
	pushMetricCmd.MarkFlagRequired("token")

	return rootCmd
}

func run() error {
	cfg, err := config.LoadMergedConfig(filepath.Dir(configPath))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := kuma.New(ctx, cfg.UptimeKumaURL, cfg.Username, cfg.Password, kuma.WithLogLevel(kuma.LogLevelInfo))
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	log.Println("Client created successfully")
	defer client.Disconnect()

	if err := provision.ProvisionKumaMonitor(ctx, client, cfg); err != nil {
		return err
	}
	log.Println("Provisioning completed successfully")

	if withTelegraf {
		log.Printf("withTelegraf flag: %t - generating configs", withTelegraf)
		if err := telegraf.GenerateTelegrafConfigs(cfg, telegrafDir); err != nil {
			return err
		}
	}

	// Force immediate exit to avoid hanging on Socket.IO goroutines
	os.Exit(0)

	return nil
}
