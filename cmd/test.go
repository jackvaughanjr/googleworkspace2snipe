package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackvaughanjr/googleworkspace2snipe/internal/googleworkspace"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/snipeit"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/sync"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Validate API connections and report current license state",
	Long: `Lists all Google Workspace SKUs with active assignments and shows
whether the corresponding Snipe-IT license already exists. No changes are made.`,
	RunE: runTest,
}

func init() {
	rootCmd.AddCommand(testCmd)
}

func runTest(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	credFile := viper.GetString("google_workspace.credentials_file")
	adminEmail := viper.GetString("google_workspace.admin_email")
	domain := viper.GetString("google_workspace.domain")

	if credFile == "" {
		return fmt.Errorf("google_workspace.credentials_file is required in settings.yaml")
	}
	if adminEmail == "" {
		return fmt.Errorf("google_workspace.admin_email is required in settings.yaml")
	}
	if domain == "" {
		return fmt.Errorf("google_workspace.domain is required in settings.yaml")
	}

	gwsClient, err := googleworkspace.NewClientFromFile(credFile, adminEmail, domain)
	if err != nil {
		return fmt.Errorf("creating Google Workspace client: %w", err)
	}
	snipeClient := snipeit.NewClient(
		viper.GetString("snipe_it.url"),
		viper.GetString("snipe_it.api_key"),
	)

	productIDs := viper.GetStringSlice("google_workspace.product_ids")
	if len(productIDs) == 0 {
		productIDs = googleworkspace.DefaultProductIDs
	}

	cfg := sync.Config{
		LicenseNamePrefix: viper.GetString("google_workspace.license_name_prefix"),
		LicenseNameSuffix: viper.GetString("google_workspace.license_name_suffix"),
	}

	// --- Google Workspace ---
	fmt.Println("=== Google Workspace ===")
	fmt.Printf("Domain:   %s\n", domain)
	fmt.Printf("Products: %v\n\n", productIDs)

	skuGroups, err := gwsClient.ListLicenseAssignmentsBySku(ctx, productIDs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Google Workspace error: %v\n", err)
		return err
	}

	if len(skuGroups) == 0 {
		fmt.Println("No active license assignments found for the configured products.")
		fmt.Println("Check google_workspace.product_ids in settings.yaml or verify DWD scopes.")
		return nil
	}

	// --- SKU → Snipe-IT mapping ---
	fmt.Println("=== SKUs → Snipe-IT Licenses ===")
	fmt.Printf("%-50s  %6s  %s\n", "Snipe-IT License Name", "Users", "Snipe-IT Status")
	fmt.Println(strings.Repeat("-", 90))

	for _, sku := range skuGroups {
		licenseName := sync.BuildLicenseName(cfg, sku.SkuName)
		lic, err := snipeClient.FindLicenseByName(ctx, licenseName)
		var status string
		if err != nil {
			status = "error: " + err.Error()
		} else if lic == nil {
			status = "not found (will be created on sync)"
		} else {
			status = fmt.Sprintf("id=%d seats=%d free=%d", lic.ID, lic.Seats, lic.FreeSeatsCount)
		}
		fmt.Printf("%-50s  %6d  %s\n", licenseName, len(sku.UserEmails), status)
	}

	return nil
}
