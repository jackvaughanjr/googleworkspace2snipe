package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackvaughanjr/googleworkspace2snipe/internal/googleworkspace"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/slack"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/snipeit"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/sync"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync active Google Workspace license assignments into Snipe-IT",
	Long: `Queries the Google Enterprise License Manager API for all active SKU
assignments and creates or updates one Snipe-IT license per SKU. Users who
lose a Google Workspace license are automatically checked back in.`,
	RunE: runSync,
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().Bool("dry-run", false, "simulate without making changes")
	syncCmd.Flags().Bool("force", false, "re-sync even if notes appear up to date")
	syncCmd.Flags().String("email", "", "sync a single user by email across all their SKUs")
	syncCmd.Flags().Bool("create-users", false, "create Snipe-IT accounts for Google Workspace users that do not already exist")
	syncCmd.Flags().Bool("no-slack", false, "suppress Slack notifications for this run")

	_ = viper.BindPFlag("sync.dry_run", syncCmd.Flags().Lookup("dry-run"))
	_ = viper.BindPFlag("sync.force", syncCmd.Flags().Lookup("force"))
	_ = viper.BindPFlag("sync.create_users", syncCmd.Flags().Lookup("create-users"))
}

func runSync(cmd *cobra.Command, args []string) error {
	credFile := viper.GetString("google_workspace.credentials_file")
	adminEmail := viper.GetString("google_workspace.admin_email")
	domain := viper.GetString("google_workspace.domain")

	if credFile == "" {
		return fatal("google_workspace.credentials_file is required in settings.yaml")
	}
	if adminEmail == "" {
		return fatal("google_workspace.admin_email is required in settings.yaml")
	}
	if domain == "" {
		return fatal("google_workspace.domain is required in settings.yaml")
	}

	categoryID := viper.GetInt("snipe_it.license_category_id")
	if categoryID == 0 {
		return fatal("snipe_it.license_category_id is required in settings.yaml")
	}

	ouPaths := viper.GetStringSlice("google_workspace.ou_paths")
	enrichSkus := viper.GetStringSlice("google_workspace.enrich_notes_for_skus")
	createUsers := viper.GetBool("sync.create_users")
	needsDirectory := len(ouPaths) > 0 || len(enrichSkus) > 0 || createUsers

	gwsClient, err := googleworkspace.NewClientFromFile(credFile, adminEmail, domain, needsDirectory)
	if err != nil {
		return fatal("creating Google Workspace client: %v", err)
	}
	snipeClient := snipeit.NewClient(
		viper.GetString("snipe_it.url"),
		viper.GetString("snipe_it.api_key"),
	)

	emailFilter, _ := cmd.Flags().GetString("email")
	noSlack, _ := cmd.Flags().GetBool("no-slack")

	cfg := sync.Config{
		DryRun:            viper.GetBool("sync.dry_run"),
		Force:             viper.GetBool("sync.force"),
		LicenseCategoryID: categoryID,
		ManufacturerID:    viper.GetInt("snipe_it.license_manufacturer_id"),
		SupplierID:        viper.GetInt("snipe_it.license_supplier_id"),
		ProductIDs:        viper.GetStringSlice("google_workspace.product_ids"),
		LicenseNamePrefix: viper.GetString("google_workspace.license_name_prefix"),
		LicenseNameSuffix: viper.GetString("google_workspace.license_name_suffix"),
		OUPaths:           ouPaths,
		EnrichNotesSKUs:   enrichSkus,
		CreateUsers:       createUsers,
	}

	if cfg.DryRun {
		slog.Info("dry-run mode enabled — no changes will be made")
	}

	slackClient := slack.NewClient(viper.GetString("slack.webhook_url"))
	ctx := context.Background()

	if err := gwsClient.ValidateAPIs(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "API access check failed: %v\n", err)
		if !cfg.DryRun && !noSlack {
			msg := fmt.Sprintf("googleworkspace2snipe sync failed: %v", err)
			if notifyErr := slackClient.Send(ctx, msg); notifyErr != nil {
				slog.Warn("slack notification failed", "error", notifyErr)
			}
		}
		return err
	}

	syncer := sync.NewSyncer(gwsClient, snipeClient, cfg)
	result, err := syncer.Run(ctx, emailFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync failed: %v\n", err)
		if !cfg.DryRun && !noSlack {
			msg := fmt.Sprintf("googleworkspace2snipe sync failed: %v", err)
			if notifyErr := slackClient.Send(ctx, msg); notifyErr != nil {
				slog.Warn("slack notification failed", "error", notifyErr)
			}
		}
		return err
	}

	if !cfg.DryRun && !noSlack {
		for _, email := range result.UnmatchedEmails {
			msg := fmt.Sprintf("googleworkspace2snipe: no Snipe-IT account found for Google Workspace user — %s", email)
			if notifyErr := slackClient.Send(ctx, msg); notifyErr != nil {
				slog.Warn("slack notification failed", "email", email, "error", notifyErr)
			}
		}

		msg := fmt.Sprintf(
			"googleworkspace2snipe sync complete — checked out: %d, notes updated: %d, checked in: %d, skipped: %d, users created: %d, warnings: %d",
			result.CheckedOut, result.NotesUpdated, result.CheckedIn, result.Skipped, result.UsersCreated, result.Warnings,
		)
		if notifyErr := slackClient.Send(ctx, msg); notifyErr != nil {
			slog.Warn("slack notification failed", "error", notifyErr)
		}
	}

	fmt.Printf("Sync complete: checked_out=%d notes_updated=%d checked_in=%d skipped=%d users_created=%d warnings=%d\n",
		result.CheckedOut, result.NotesUpdated, result.CheckedIn, result.Skipped, result.UsersCreated, result.Warnings)
	return nil
}
