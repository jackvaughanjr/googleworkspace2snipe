package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackvaughanjr/googleworkspace2snipe/internal/googleworkspace"
	"github.com/jackvaughanjr/googleworkspace2snipe/internal/snipeit"
)

// Config controls sync behaviour.
type Config struct {
	DryRun            bool
	Force             bool
	LicenseCategoryID int
	// ManufacturerID is optional. If 0, auto find/create "Google" as the manufacturer.
	ManufacturerID int
	// SupplierID is optional. If 0, no supplier is set on created licenses.
	SupplierID int
	// ProductIDs is the list of Google Workspace product IDs to query.
	// Defaults to googleworkspace.DefaultProductIDs when empty.
	ProductIDs []string
	// LicenseNamePrefix is prepended verbatim to each SKU name in Snipe-IT.
	// Example: "Acme - " produces "Acme - Google Workspace Business Plus".
	LicenseNamePrefix string
	// LicenseNameSuffix is appended verbatim to each SKU name in Snipe-IT.
	// Example: " (acme.com)" produces "Google Workspace Business Plus (acme.com)".
	LicenseNameSuffix string
}

// Syncer orchestrates the Google Workspace → Snipe-IT per-SKU license sync.
type Syncer struct {
	gws    *googleworkspace.Client
	snipe  *snipeit.Client
	config Config
}

func NewSyncer(gws *googleworkspace.Client, snipe *snipeit.Client, cfg Config) *Syncer {
	return &Syncer{gws: gws, snipe: snipe, config: cfg}
}

// Run executes the full sync across all SKUs found in the configured product IDs.
// Each SKU with at least one active assignment is synced as a separate Snipe-IT license.
//
// emailFilter restricts the checkout pass to one user across all SKUs they appear in,
// and suppresses the checkin pass.
func (s *Syncer) Run(ctx context.Context, emailFilter string) (Result, error) {
	var result Result

	productIDs := s.config.ProductIDs
	if len(productIDs) == 0 {
		productIDs = googleworkspace.DefaultProductIDs
	}

	// Resolve manufacturer once, reused across all SKU license creates.
	manufacturerID := s.config.ManufacturerID
	if !s.config.DryRun && manufacturerID == 0 {
		mfr, err := s.snipe.FindOrCreateManufacturer(ctx, "Google", "https://workspace.google.com")
		if err != nil {
			return result, fmt.Errorf("resolving Google manufacturer in Snipe-IT: %w", err)
		}
		manufacturerID = mfr.ID
		slog.Info("resolved manufacturer", "name", "Google", "id", manufacturerID)
	}

	slog.Info("fetching Google Workspace license assignments", "products", productIDs)
	skuGroups, err := s.gws.ListLicenseAssignmentsBySku(ctx, productIDs)
	if err != nil {
		return result, fmt.Errorf("listing license assignments: %w", err)
	}
	slog.Info("found SKUs with active assignments", "count", len(skuGroups))

	seenUnmatched := make(map[string]struct{})

	for _, sku := range skuGroups {
		skuResult, err := s.syncSku(ctx, sku, emailFilter, manufacturerID)
		if err != nil {
			slog.Warn("error syncing SKU", "sku", sku.SkuName, "error", err)
			result.Warnings++
			continue
		}
		result.CheckedOut += skuResult.CheckedOut
		result.NotesUpdated += skuResult.NotesUpdated
		result.CheckedIn += skuResult.CheckedIn
		result.Skipped += skuResult.Skipped
		result.Warnings += skuResult.Warnings
		// Deduplicate unmatched emails across SKUs — a user missing from Snipe-IT
		// should only trigger one notification, not one per SKU they hold.
		for _, email := range skuResult.UnmatchedEmails {
			if _, seen := seenUnmatched[email]; !seen {
				seenUnmatched[email] = struct{}{}
				result.UnmatchedEmails = append(result.UnmatchedEmails, email)
			}
		}
	}

	return result, nil
}

// syncSku syncs a single Google Workspace SKU to one Snipe-IT license.
func (s *Syncer) syncSku(ctx context.Context, sku googleworkspace.SkuGroup, emailFilter string, manufacturerID int) (Result, error) {
	var result Result
	licenseName := BuildLicenseName(s.config, sku.SkuName)

	// Full active email set for this SKU — used in the checkin pass regardless
	// of the --email filter.
	activeEmails := make(map[string]struct{}, len(sku.UserEmails))
	for _, email := range sku.UserEmails {
		activeEmails[strings.ToLower(email)] = struct{}{}
	}

	// Apply --email filter to the checkout list.
	checkoutEmails := sku.UserEmails
	if emailFilter != "" {
		needle := strings.ToLower(emailFilter)
		var filtered []string
		for _, email := range checkoutEmails {
			if strings.ToLower(email) == needle {
				filtered = append(filtered, email)
				break
			}
		}
		if len(filtered) == 0 {
			// User doesn't hold this SKU; skip entirely.
			return result, nil
		}
		checkoutEmails = filtered
	}

	slog.Info("syncing SKU", "sku", sku.SkuName, "license", licenseName, "users", len(sku.UserEmails))

	notes := buildNotes(sku)

	// Find or create the Snipe-IT license for this SKU.
	var (
		lic *snipeit.License
		err error
	)
	activeCount := len(sku.UserEmails)
	if s.config.DryRun {
		lic, err = s.snipe.FindLicenseByName(ctx, licenseName)
		if err != nil {
			return result, err
		}
		if lic == nil {
			slog.Info("[dry-run] license not found; would be created",
				"license", licenseName, "seats", activeCount)
			lic = &snipeit.License{Name: licenseName, Seats: activeCount}
		}
	} else {
		lic, err = s.snipe.FindOrCreateLicense(ctx, licenseName, activeCount,
			s.config.LicenseCategoryID, manufacturerID, s.config.SupplierID)
		if err != nil {
			return result, err
		}
	}
	slog.Info("license resolved", "license", licenseName, "id", lic.ID,
		"seats", lic.Seats, "free", lic.FreeSeatsCount)

	// Expand seats if needed (never shrink automatically).
	if activeCount > lic.Seats {
		slog.Info("expanding license seats", "license", licenseName,
			"current", lic.Seats, "needed", activeCount)
		if !s.config.DryRun {
			lic, err = s.snipe.UpdateLicenseSeats(ctx, lic.ID, activeCount)
			if err != nil {
				return result, err
			}
		}
	}

	// Load current seat assignments.
	checkedOutByEmail := make(map[string]*snipeit.LicenseSeat)
	var freeSeats []*snipeit.LicenseSeat
	if lic.ID != 0 {
		seats, err := s.snipe.ListLicenseSeats(ctx, lic.ID)
		if err != nil {
			return result, err
		}
		for i := range seats {
			seat := &seats[i]
			if seat.AssignedTo != nil && seat.AssignedTo.Email != "" {
				checkedOutByEmail[strings.ToLower(seat.AssignedTo.Email)] = seat
			} else {
				freeSeats = append(freeSeats, seat)
			}
		}
	} else if !s.config.DryRun {
		return result, fmt.Errorf("license %q resolved with id=0 in production mode — check Snipe-IT API permissions", licenseName)
	}
	slog.Debug("seat state loaded", "license", licenseName,
		"checked_out", len(checkedOutByEmail), "free", len(freeSeats))

	// Checkout / update loop.
	for _, email := range checkoutEmails {
		snipeUser, err := s.snipe.FindUserByEmail(ctx, email)
		if err != nil {
			slog.Warn("error looking up Snipe-IT user", "email", email, "error", err)
			result.Warnings++
			continue
		}
		if snipeUser == nil {
			slog.Warn("no Snipe-IT user found for Google Workspace user", "email", email)
			result.UnmatchedEmails = append(result.UnmatchedEmails, email)
			result.Warnings++
			continue
		}

		if existing, ok := checkedOutByEmail[email]; ok {
			if existing.Notes == notes && !s.config.Force {
				slog.Debug("seat up to date", "email", email, "license", licenseName)
				result.Skipped++
				continue
			}
			slog.Info("updating seat notes", "email", email, "license", licenseName,
				"dry_run", s.config.DryRun)
			if !s.config.DryRun {
				if err := s.snipe.UpdateSeatNotes(ctx, lic.ID, existing.ID, notes); err != nil {
					slog.Warn("failed to update seat notes", "email", email,
						"license", licenseName, "error", err)
					result.Warnings++
					continue
				}
			}
			result.NotesUpdated++
			continue
		}

		if s.config.DryRun {
			slog.Info("[dry-run] would check out seat", "email", email, "license", licenseName)
			result.CheckedOut++
			continue
		}
		if len(freeSeats) == 0 {
			slog.Warn("no free seats available", "email", email, "license", licenseName)
			result.Warnings++
			continue
		}
		seat := freeSeats[0]
		freeSeats = freeSeats[1:]

		slog.Info("checking out seat", "email", email, "license", licenseName, "seat_id", seat.ID)
		if err := s.snipe.CheckoutSeat(ctx, lic.ID, seat.ID, snipeUser.ID, notes); err != nil {
			slog.Warn("failed to checkout seat", "email", email, "license", licenseName, "error", err)
			freeSeats = append(freeSeats, seat) // return on failure
			result.Warnings++
			continue
		}
		result.CheckedOut++
	}

	// Checkin loop — skip when --email filter is set.
	if emailFilter == "" {
		for email, seat := range checkedOutByEmail {
			if _, active := activeEmails[email]; active {
				continue
			}
			slog.Info("checking in seat for inactive user", "email", email,
				"license", licenseName, "seat_id", seat.ID, "dry_run", s.config.DryRun)
			if !s.config.DryRun {
				if err := s.snipe.CheckinSeat(ctx, lic.ID, seat.ID); err != nil {
					slog.Warn("failed to checkin seat", "email", email,
						"license", licenseName, "error", err)
					result.Warnings++
					continue
				}
			}
			result.CheckedIn++
		}
	}

	return result, nil
}

// BuildLicenseName applies the configured prefix and suffix to a SKU name.
// The prefix and suffix are concatenated verbatim — include any desired
// separators or punctuation in the prefix/suffix values themselves.
func BuildLicenseName(cfg Config, skuName string) string {
	return cfg.LicenseNamePrefix + skuName + cfg.LicenseNameSuffix
}

// buildNotes returns the notes string written to each Snipe-IT seat.
// The product and SKU IDs are stable identifiers useful for debugging.
func buildNotes(sku googleworkspace.SkuGroup) string {
	return "product_id: " + sku.ProductID + "\nsku_id: " + sku.SkuID
}
