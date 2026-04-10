package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackvaughanjr/googleworkspace2snipe/internal/googleworkspace"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Discover Google Workspace product IDs with active license assignments",
	Long: `Probes all known Google Workspace product IDs for active license assignments
and writes the discovered list back into settings.yaml. Run this to automatically
populate google_workspace.product_ids, especially when you have add-ons or newer
products that fall outside the built-in default list.`,
	RunE: runDiscover,
}

func init() {
	rootCmd.AddCommand(discoverCmd)
	discoverCmd.Flags().Bool("dry-run", false, "print discovered product IDs without writing settings.yaml")
}

func runDiscover(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

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

	dryRun, _ := cmd.Flags().GetBool("dry-run")

	gwsClient, err := googleworkspace.NewClientFromFile(credFile, adminEmail, domain, false)
	if err != nil {
		return fatal("creating Google Workspace client: %v", err)
	}

	if err := gwsClient.ValidateAPIs(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "API access check failed: %v\n", err)
		return err
	}

	fmt.Printf("Probing %d known Google Workspace product IDs...\n\n", len(googleworkspace.KnownProductIDs))

	var active []string
	for _, pid := range googleworkspace.KnownProductIDs {
		has, err := gwsClient.ProbeProductHasAssignments(ctx, pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %-40s  error: %v\n", pid, err)
			return err
		}
		if has {
			fmt.Printf("  %-40s  active\n", pid)
			active = append(active, pid)
		} else {
			fmt.Printf("  %-40s  not found\n", pid)
		}
	}
	fmt.Println()

	if len(active) == 0 {
		fmt.Println("No active product IDs found.")
		fmt.Println("Verify that the Enterprise License Manager API is enabled and the service account has DWD grants.")
		return nil
	}

	fmt.Printf("Active product IDs (%d):\n", len(active))
	for _, pid := range active {
		fmt.Printf("  - %s\n", pid)
	}
	fmt.Println()

	if dryRun {
		fmt.Println("[dry-run] settings.yaml not updated.")
		return nil
	}

	if err := writeProductIDsToConfig(cfgFile, active); err != nil {
		return fatal("updating %s: %v", cfgFile, err)
	}
	fmt.Printf("Updated google_workspace.product_ids in %s\n", cfgFile)
	return nil
}

// writeProductIDsToConfig replaces the product_ids list in the YAML config file
// with the given product IDs, preserving all other content and comments.
func writeProductIDsToConfig(configFile string, productIDs []string) error {
	f, err := os.Open(configFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configFile, err)
	}
	defer f.Close()

	var out strings.Builder
	scanner := bufio.NewScanner(f)
	inProductIDs := false
	injected := false

	for scanner.Scan() {
		line := scanner.Text()

		if inProductIDs {
			if isProductIDsItem(strings.TrimSpace(line)) {
				continue // drop old items
			}
			inProductIDs = false
		}

		out.WriteString(line)
		out.WriteByte('\n')

		if !injected && strings.HasPrefix(line, "  product_ids:") {
			inProductIDs = true
			injected = true
			for _, pid := range productIDs {
				out.WriteString(fmt.Sprintf("    - %q\n", pid))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	if !injected {
		return fmt.Errorf("product_ids key not found in %s — edit google_workspace.product_ids manually", configFile)
	}

	return os.WriteFile(configFile, []byte(out.String()), 0644)
}

// isProductIDsItem reports whether a trimmed YAML line is a list item belonging
// to the product_ids block — either active ("- ...") or commented out ("# - ...").
func isProductIDsItem(trimmed string) bool {
	if strings.HasPrefix(trimmed, "- ") {
		return true
	}
	if strings.HasPrefix(trimmed, "#") {
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		return strings.HasPrefix(rest, "- ")
	}
	return false
}
