package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/jackvaughanjr/googleworkspace2snipe/internal/googleworkspace"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/snipeit"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Validate API connections and report current state",
	RunE:  runTest,
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

	gwsClient, err := googleworkspace.NewClientFromFile(credFile, adminEmail, domain, ouPaths)
	if err != nil {
		return fmt.Errorf("creating Google Workspace client: %w", err)
	}
	snipeClient := snipeit.NewClient(
		viper.GetString("snipe_it.url"),
		viper.GetString("snipe_it.api_key"),
	)

	// --- Google Workspace ---
	fmt.Println("=== Google Workspace ===")
	if len(ouPaths) > 0 {
		fmt.Printf("OU filter: %v\n", ouPaths)
	}
	users, err := gwsClient.ListActiveUsers(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Google Workspace error: %v\n", err)
		return err
	}
	fmt.Printf("Active users: %d\n", len(users))

	adminCount := 0
	for _, u := range users {
		if u.IsAdmin {
			adminCount++
		}
	}
	fmt.Printf("Super admins: %d\n", adminCount)

	// --- Snipe-IT ---
	fmt.Println("\n=== Snipe-IT ===")
	licenseName := viper.GetString("snipe_it.license_name")
	if licenseName == "" {
		licenseName = "Google Workspace"
	}

	lic, err := snipeClient.FindLicenseByName(ctx, licenseName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Snipe-IT error: %v\n", err)
		return err
	}
	if lic == nil {
		fmt.Printf("License %q: not found (will be created on first sync)\n", licenseName)
	} else {
		fmt.Printf("License %q: id=%d seats=%d free=%d\n",
			lic.Name, lic.ID, lic.Seats, lic.FreeSeatsCount)
	}

	return nil
}
