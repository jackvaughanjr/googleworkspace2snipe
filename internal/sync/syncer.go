package sync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	LicenseNamePrefix string
	// LicenseNameSuffix is appended verbatim to each SKU name in Snipe-IT.
	LicenseNameSuffix string
	// OUPaths restricts the sync to users in these Organizational Units (and
	// their subtrees). Users outside the specified OUs are ignored for both
	// checkout and checkin. Empty = all users in the domain.
	OUPaths []string
	// EnrichNotesSKUs is a list of SKU names or SKU IDs whose Snipe-IT seat
	// notes should include per-user OU path and admin status in addition to
	// the standard product/SKU identifiers. Matching is case-insensitive against
	// either the SKU name (e.g. "Google Workspace Business Plus") or the SKU ID
	// (e.g. "1010020025").
	EnrichNotesSKUs []string
	// CreateUsers controls whether new Snipe-IT users are created for Google
	// Workspace users that have no matching Snipe-IT account. Created users have
	// login disabled, no welcome email, and their start date set to the Google
	// Workspace account creation date. Requires the Directory API scope.
	CreateUsers bool
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
// Each SKU with at least one active assignment becomes a separate Snipe-IT license.
//
// emailFilter restricts the checkout pass to one user across all their SKUs
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

	// Fetch the Directory API user map when OU filtering or note enrichment is
	// configured. The map is keyed by lowercased email and provides OrgUnitPath
	// and IsAdmin for each active user in scope.
	needsDirectory := len(s.config.OUPaths) > 0 || len(s.config.EnrichNotesSKUs) > 0 || s.config.CreateUsers
	var userMap map[string]googleworkspace.User
	if needsDirectory {
		slog.Info("fetching Directory API user map",
			"ou_filter", s.config.OUPaths,
			"enrich_skus", s.config.EnrichNotesSKUs)
		var err error
		userMap, err = s.gws.GetUserMap(ctx, s.config.OUPaths)
		if err != nil {
			return result, fmt.Errorf("fetching user directory: %w", err)
		}
		slog.Info("user map loaded", "users", len(userMap))
	}

	// Build the allowed-email set for OU filtering (nil = no filter).
	var allowedEmails map[string]struct{}
	if len(s.config.OUPaths) > 0 {
		allowedEmails = make(map[string]struct{}, len(userMap))
		for email := range userMap {
			allowedEmails[email] = struct{}{}
		}
		slog.Info("OU filter active", "allowed_users", len(allowedEmails))
	}

	slog.Info("fetching Google Workspace license assignments", "products", productIDs)
	skuGroups, err := s.gws.ListLicenseAssignmentsBySku(ctx, productIDs)
	if err != nil {
		return result, fmt.Errorf("listing license assignments: %w", err)
	}
	slog.Info("found SKUs with active assignments", "count", len(skuGroups))

	seenUnmatched := make(map[string]struct{})

	for _, sku := range skuGroups {
		skuResult, err := s.syncSku(ctx, sku, emailFilter, manufacturerID, allowedEmails, userMap)
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
		result.UsersCreated += skuResult.UsersCreated
		// Deduplicate unmatched emails across SKUs.
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
//
// allowedEmails: set of emails within the OU filter scope (nil = no filter).
// userMap: Directory API user details, used for OU/admin note enrichment (nil = no enrichment).
func (s *Syncer) syncSku(
	ctx context.Context,
	sku googleworkspace.SkuGroup,
	emailFilter string,
	manufacturerID int,
	allowedEmails map[string]struct{},
	userMap map[string]googleworkspace.User,
) (Result, error) {
	var result Result
	licenseName := BuildLicenseName(s.config, sku.SkuName)
	enriched := IsEnrichedSKU(s.config, sku)

	// Apply OU filter: restrict to users within the allowed set.
	managedEmails := sku.UserEmails
	if allowedEmails != nil {
		var filtered []string
		for _, email := range managedEmails {
			if _, ok := allowedEmails[email]; ok {
				filtered = append(filtered, email)
			}
		}
		managedEmails = filtered
	}

	if len(managedEmails) == 0 && allowedEmails != nil {
		// No users in this SKU fall within the OU filter; skip entirely.
		slog.Debug("SKU has no users in OU scope, skipping", "sku", sku.SkuName)
		return result, nil
	}

	// activeEmails drives the checkin pass: users we manage who currently hold
	// this SKU. Users outside allowedEmails (if set) are never touched.
	activeEmails := make(map[string]struct{}, len(managedEmails))
	for _, email := range managedEmails {
		activeEmails[email] = struct{}{}
	}

	// Apply --email filter to the checkout list.
	checkoutEmails := managedEmails
	if emailFilter != "" {
		needle := strings.ToLower(emailFilter)
		var filtered []string
		for _, email := range checkoutEmails {
			if email == needle {
				filtered = append(filtered, email)
				break
			}
		}
		if len(filtered) == 0 {
			return result, nil // user not in this SKU (or not in OU scope)
		}
		checkoutEmails = filtered
	}

	slog.Info("syncing SKU", "sku", sku.SkuName, "license", licenseName,
		"managed_users", len(managedEmails), "enriched_notes", enriched)

	// Find or create the Snipe-IT license.
	activeCount := len(managedEmails)
	var (
		lic *snipeit.License
		err error
	)
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
		notes := buildNotes(sku, enriched, email, userMap)

		snipeUser, err := s.snipe.FindUserByEmail(ctx, email)
		if err != nil {
			slog.Warn("error looking up Snipe-IT user", "email", email, "error", err)
			result.Warnings++
			continue
		}
		if snipeUser == nil {
			if s.config.CreateUsers {
				if s.config.DryRun {
					slog.Info("[dry-run] would create Snipe-IT user", "email", email)
					result.UsersCreated++
					result.CheckedOut++ // would proceed to checkout
					continue
				}
				snipeUser, err = s.createSnipeUser(ctx, email, userMap)
				if err != nil {
					slog.Warn("failed to create Snipe-IT user", "email", email, "error", err)
					result.Warnings++
					continue
				}
				result.UsersCreated++
			} else {
				slog.Warn("no Snipe-IT user found for Google Workspace user", "email", email)
				result.UnmatchedEmails = append(result.UnmatchedEmails, email)
				result.Warnings++
				continue
			}
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
			freeSeats = append(freeSeats, seat)
			result.Warnings++
			continue
		}
		result.CheckedOut++
	}

	// Checkin loop — skip when --email filter is set.
	if emailFilter == "" {
		for email, seat := range checkedOutByEmail {
			if _, active := activeEmails[email]; active {
				continue // still holds the SKU
			}
			// If OU filter is active, only touch seats for users within scope.
			if allowedEmails != nil {
				if _, inScope := allowedEmails[email]; !inScope {
					continue
				}
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

// createSnipeUser creates a new Snipe-IT user for a Google Workspace email.
// User details (name, creation date) are pulled from userMap when available.
// The created user has login disabled, no welcome email, and notes that
// document the auto-creation source.
func (s *Syncer) createSnipeUser(ctx context.Context, email string, userMap map[string]googleworkspace.User) (*snipeit.SnipeUser, error) {
	firstName, lastName, startDate := "", "", ""

	if u, ok := userMap[email]; ok {
		firstName = u.Name.GivenName
		lastName = u.Name.FamilyName
		if u.CreationTime != "" {
			if t, err := time.Parse(time.RFC3339, u.CreationTime); err == nil {
				startDate = t.Format("2006-01-02")
			} else if t, err := time.Parse("2006-01-02T15:04:05.000Z", u.CreationTime); err == nil {
				startDate = t.Format("2006-01-02")
			}
		}
	}

	// Fall back to deriving a name from the email local-part when the Directory
	// API entry is missing (e.g. user outside the OU filter scope).
	if firstName == "" && lastName == "" {
		local := strings.SplitN(email, "@", 2)[0]
		parts := strings.SplitN(local, ".", 2)
		firstName = parts[0]
		if len(parts) == 2 {
			lastName = parts[1]
		}
	}

	notes := "Auto-created from Google Workspace via googleworkspace2snipe"
	// Use the full email as username — it is globally unique within Snipe-IT.
	username := email

	slog.Info("creating Snipe-IT user", "email", email, "start_date", startDate)
	return s.snipe.CreateUser(ctx, firstName, lastName, email, username, notes, startDate)
}

// BuildLicenseName applies the configured prefix and suffix to a SKU name.
func BuildLicenseName(cfg Config, skuName string) string {
	return cfg.LicenseNamePrefix + skuName + cfg.LicenseNameSuffix
}

// IsEnrichedSKU reports whether this SKU should include per-user OU and admin
// details in its Snipe-IT seat notes. Matches against either SKU name or SKU ID,
// case-insensitively.
func IsEnrichedSKU(cfg Config, sku googleworkspace.SkuGroup) bool {
	for _, s := range cfg.EnrichNotesSKUs {
		if strings.EqualFold(s, sku.SkuName) || strings.EqualFold(s, sku.SkuID) {
			return true
		}
	}
	return false
}

// buildNotes returns the notes string written to the Snipe-IT seat.
// For enriched SKUs, per-user OU path and admin status are appended using data
// from the Directory API user map (if available for that user).
func buildNotes(sku googleworkspace.SkuGroup, enriched bool, email string, userMap map[string]googleworkspace.User) string {
	base := "product_id: " + sku.ProductID + "\nsku_id: " + sku.SkuID
	if !enriched || userMap == nil {
		return base
	}
	u, ok := userMap[email]
	if !ok {
		return base
	}
	adminStr := "false"
	if u.IsAdmin {
		adminStr = "true"
	}
	return base + "\norg_unit: " + u.OrgUnitPath + "\nis_admin: " + adminStr
}
