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
	LicenseName       string
	LicenseCategoryID int
	// ManufacturerID is optional. If 0, auto find/create "Google" as the manufacturer.
	ManufacturerID int
	// SupplierID is optional. If 0, no supplier is set on the license.
	SupplierID int
}

// Syncer orchestrates the Google Workspace → Snipe-IT license sync.
type Syncer struct {
	gws    *googleworkspace.Client
	snipe  *snipeit.Client
	config Config
}

func NewSyncer(gws *googleworkspace.Client, snipe *snipeit.Client, cfg Config) *Syncer {
	return &Syncer{gws: gws, snipe: snipe, config: cfg}
}

// Run executes the full sync. emailFilter restricts the checkout pass to one
// user (and skips the checkin pass entirely).
func (s *Syncer) Run(ctx context.Context, emailFilter string) (Result, error) {
	var result Result

	// 1. Fetch active users from Google Workspace (paginated internally).
	slog.Info("fetching active Google Workspace users")
	activeUsers, err := s.gws.ListActiveUsers(ctx)
	if err != nil {
		return result, fmt.Errorf("listing Google Workspace users: %w", err)
	}
	slog.Info("fetched active users", "count", len(activeUsers))

	// 2. Build active email set (used in the checkin pass).
	activeEmails := make(map[string]struct{}, len(activeUsers))
	for _, u := range activeUsers {
		activeEmails[emailKey(u)] = struct{}{}
	}

	// 3. Apply --email filter.
	if emailFilter != "" {
		needle := strings.ToLower(emailFilter)
		filtered := activeUsers[:0]
		for _, u := range activeUsers {
			if emailKey(u) == needle {
				filtered = append(filtered, u)
				break
			}
		}
		activeUsers = filtered
		slog.Info("filtered to single user", "email", emailFilter, "found", len(activeUsers) > 0)
	}

	// 4. No separate metadata fetch needed — all relevant data (OrgUnitPath,
	// IsAdmin) is included in the user objects returned by ListActiveUsers.

	// 5. Resolve manufacturer. If ManufacturerID is 0, auto find/create "Google".
	manufacturerID := s.config.ManufacturerID
	if !s.config.DryRun && manufacturerID == 0 {
		mfr, err := s.snipe.FindOrCreateManufacturer(ctx, "Google", "https://workspace.google.com")
		if err != nil {
			return result, fmt.Errorf("resolving Google manufacturer in Snipe-IT: %w", err)
		}
		manufacturerID = mfr.ID
		slog.Info("resolved manufacturer", "name", "Google", "id", manufacturerID)
	}

	// 6. Find or create the license.
	// Dry-run: find only; synthesize placeholder if not found (id=0).
	slog.Info("finding or creating license", "name", s.config.LicenseName)
	var lic *snipeit.License
	activeCount := len(activeEmails)
	if s.config.DryRun {
		lic, err = s.snipe.FindLicenseByName(ctx, s.config.LicenseName)
		if err != nil {
			return result, err
		}
		if lic == nil {
			slog.Info("[dry-run] license not found; would be created", "name", s.config.LicenseName, "seats", activeCount)
			lic = &snipeit.License{Name: s.config.LicenseName, Seats: activeCount}
		}
	} else {
		lic, err = s.snipe.FindOrCreateLicense(ctx, s.config.LicenseName, activeCount, s.config.LicenseCategoryID, manufacturerID, s.config.SupplierID)
		if err != nil {
			return result, err
		}
	}
	slog.Info("license resolved", "id", lic.ID, "seats", lic.Seats, "free", lic.FreeSeatsCount)

	// 7. Expand seats if needed (never shrink automatically).
	if activeCount > lic.Seats {
		slog.Info("expanding license seats", "current", lic.Seats, "needed", activeCount)
		if !s.config.DryRun {
			lic, err = s.snipe.UpdateLicenseSeats(ctx, lic.ID, activeCount)
			if err != nil {
				return result, err
			}
		}
	}

	// 8. Load current seat assignments.
	// Dry-run with a synthetic license (id=0) skips the API call.
	// In production, id=0 means something went wrong — fail fast.
	checkedOutByEmail := make(map[string]*snipeit.LicenseSeat)
	var freeSeats []*snipeit.LicenseSeat
	if lic.ID != 0 {
		slog.Info("loading current seat assignments")
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
		return result, fmt.Errorf("license resolved with id=0 in production mode — check Snipe-IT API permissions and required fields")
	} else {
		slog.Info("[dry-run] skipping seat load for new license")
	}
	slog.Info("seat state loaded", "checked_out", len(checkedOutByEmail), "free", len(freeSeats))

	// 9. Checkout / update loop.
	for _, u := range activeUsers {
		email := emailKey(u)
		notes := buildNotes(u)

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
				slog.Debug("seat up to date", "email", email)
				result.Skipped++
				continue
			}
			slog.Info("updating seat notes", "email", email, "dry_run", s.config.DryRun)
			if !s.config.DryRun {
				if err := s.snipe.UpdateSeatNotes(ctx, lic.ID, existing.ID, notes); err != nil {
					slog.Warn("failed to update seat notes", "email", email, "error", err)
					result.Warnings++
					continue
				}
			}
			result.NotesUpdated++
			continue
		}

		if s.config.DryRun {
			slog.Info("[dry-run] would check out seat", "email", email, "notes", notes)
			result.CheckedOut++
			continue
		}
		if len(freeSeats) == 0 {
			slog.Warn("no free seats available", "email", email)
			result.Warnings++
			continue
		}
		seat := freeSeats[0]
		freeSeats = freeSeats[1:]

		slog.Info("checking out seat", "email", email, "seat_id", seat.ID)
		if err := s.snipe.CheckoutSeat(ctx, lic.ID, seat.ID, snipeUser.ID, notes); err != nil {
			slog.Warn("failed to checkout seat", "email", email, "error", err)
			freeSeats = append(freeSeats, seat) // return on failure
			result.Warnings++
			continue
		}
		result.CheckedOut++
	}

	// 10. Checkin loop — skip when --email filter is set.
	if emailFilter == "" {
		for email, seat := range checkedOutByEmail {
			if _, active := activeEmails[email]; active {
				continue
			}
			slog.Info("checking in seat for inactive user", "email", email, "seat_id", seat.ID, "dry_run", s.config.DryRun)
			if !s.config.DryRun {
				if err := s.snipe.CheckinSeat(ctx, lic.ID, seat.ID); err != nil {
					slog.Warn("failed to checkin seat", "email", email, "error", err)
					result.Warnings++
					continue
				}
			}
			result.CheckedIn++
		}
	}

	return result, nil
}

// emailKey returns the canonical (lowercased) email for a user.
func emailKey(u googleworkspace.User) string {
	return strings.ToLower(u.PrimaryEmail)
}

// buildNotes returns the formatted notes string written to the Snipe-IT seat.
// Includes the user's OU path and admin status.
func buildNotes(u googleworkspace.User) string {
	adminStr := "false"
	if u.IsAdmin {
		adminStr = "true"
	}
	return "org_unit: " + u.OrgUnitPath + "\nis_admin: " + adminStr
}
