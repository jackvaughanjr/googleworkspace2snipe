package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "googleworkspace2snipe",
	Short: "Sync active Google Workspace users into Snipe-IT as license seat assignments",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Suppress usage and cobra's duplicate "Error: ..." echo for all runtime
		// errors. Flag parsing errors (wrong flags, unknown commands) still show
		// usage because they fire before PersistentPreRunE runs.
		cmd.Root().SilenceUsage = true
		cmd.Root().SilenceErrors = true
		initLogging()
		return nil
	},
}

// SetVersion injects the build-time version string into the root command.
func SetVersion(v string) {
	rootCmd.Version = v
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// fatal prints an error to stderr and returns it. Use instead of bare
// return fmt.Errorf(...) in RunE so errors are visible when SilenceErrors is set.
func fatal(format string, a ...any) error {
	err := fmt.Errorf(format, a...)
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	return err
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "settings.yaml", "path to config file")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "INFO-level logging")
	rootCmd.PersistentFlags().BoolP("debug", "d", false, "DEBUG-level logging")
	rootCmd.PersistentFlags().String("log-file", "", "append logs to this file")
	rootCmd.PersistentFlags().String("log-format", "text", "log format: text or json")

	_ = viper.BindPFlag("log.verbose", rootCmd.PersistentFlags().Lookup("verbose"))
	_ = viper.BindPFlag("log.debug", rootCmd.PersistentFlags().Lookup("debug"))
	_ = viper.BindPFlag("log.file", rootCmd.PersistentFlags().Lookup("log-file"))
	_ = viper.BindPFlag("log.format", rootCmd.PersistentFlags().Lookup("log-format"))
}

func initConfig() {
	viper.SetConfigFile(cfgFile)
	viper.SetConfigType("yaml")

	viper.BindEnv("google_workspace.credentials_file", "GOOGLE_CREDENTIALS_FILE")
	viper.BindEnv("google_workspace.admin_email", "GOOGLE_ADMIN_EMAIL")
	viper.BindEnv("google_workspace.domain", "GOOGLE_DOMAIN")
	viper.BindEnv("snipe_it.url", "SNIPE_URL")
	viper.BindEnv("snipe_it.api_key", "SNIPE_TOKEN")
	viper.BindEnv("slack.webhook_url", "SLACK_WEBHOOK")
	_ = viper.BindEnv("sync.rate_limit_ms", "SNIPE_RATE_LIMIT_MS")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("could not read config file", "error", err)
		}
	}
}

func initLogging() {
	level := slog.LevelWarn
	if viper.GetBool("log.debug") {
		level = slog.LevelDebug
	} else if viper.GetBool("log.verbose") {
		level = slog.LevelInfo
	}

	var w io.Writer = os.Stderr
	if path := viper.GetString("log.file"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			slog.Warn("could not open log file", "path", path, "error", err)
		} else {
			w = io.MultiWriter(os.Stderr, f)
		}
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if viper.GetString("log.format") == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(handler))
}
