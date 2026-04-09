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

	ouPaths := viper.GetStringSlice("google_workspace.ou_paths")
	enrichSkus := viper.GetStringSlice("google_workspace.enrich_notes_for_skus")
	needsDirectory := len(ouPaths) > 0 || len(enrichSkus) > 0

	gwsClient, err := googleworkspace.NewClientFromFile(credFile, adminEmail, domain, needsDirectory)
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
		EnrichNotesSKUs:   enrichSkus,
	}

	// --- Google Workspace ---
	fmt.Println("=== Google Workspace ===")
	fmt.Printf("Domain:   %s\n", domain)
	fmt.Printf("Products: %v\n", productIDs)
	if len(ouPaths) > 0 {
		fmt.Printf("OU filter: %v\n", ouPaths)
	}
	if len(enrichSkus) > 0 {
		fmt.Printf("Enriched notes for: %v\n", enrichSkus)
	}
	fmt.Println()

	// Fetch user map if OU filter or enrichment is configured, to show filtered counts.
	var userMap map[string]googleworkspace.User
	if needsDirectory {
		userMap, err = gwsClient.GetUserMap(ctx, ouPaths)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Directory API error: %v\n", err)
			return err
		}
		if len(ouPaths) > 0 {
			fmt.Printf("Users in OU scope: %d\n\n", len(userMap))
		}
	}

	if err := gwsClient.ValidateAPIs(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "API access check failed: %v\n", err)
		return err
	}

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
	fmt.Printf("%-50s  %6s  %8s  %s\n", "Snipe-IT License Name", "Users", "In Scope", "Snipe-IT Status")
	fmt.Println(strings.Repeat("-", 100))

	for _, sku := range skuGroups {
		licenseName := sync.BuildLicenseName(cfg, sku.SkuName)

		// Count users in OU scope for this SKU.
		inScope := len(sku.UserEmails)
		if userMap != nil && len(ouPaths) > 0 {
			inScope = 0
			for _, email := range sku.UserEmails {
				if _, ok := userMap[email]; ok {
					inScope++
				}
			}
		}

		enrichedMarker := ""
		if sync.IsEnrichedSKU(cfg, sku) {
			enrichedMarker = " *"
		}

		lic, err := snipeClient.FindLicenseByName(ctx, licenseName)
		var status string
		if err != nil {
			status = "error: " + err.Error()
		} else if lic == nil {
			status = "not found (will be created on sync)"
		} else {
			status = fmt.Sprintf("id=%d seats=%d free=%d", lic.ID, lic.Seats, lic.FreeSeatsCount)
		}

		scopeStr := fmt.Sprintf("%d", inScope)
		if len(ouPaths) == 0 {
			scopeStr = "—"
		}
		fmt.Printf("%-50s  %6d  %8s  %s%s\n",
			licenseName, len(sku.UserEmails), scopeStr, status, enrichedMarker)
	}

	if len(enrichSkus) > 0 {
		fmt.Println("\n* enriched notes (org_unit + is_admin included)")
	}

	return nil
}
