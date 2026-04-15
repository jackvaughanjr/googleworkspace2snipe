# googleworkspace2snipe

[![Latest Release](https://img.shields.io/github/v/release/jackvaughanjr/googleworkspace2snipe)](https://github.com/jackvaughanjr/googleworkspace2snipe/releases/latest) [![Go Version](https://img.shields.io/github/go-mod/go-version/jackvaughanjr/googleworkspace2snipe)](go.mod) [![License](https://img.shields.io/github/license/jackvaughanjr/googleworkspace2snipe)](LICENSE) [![Build](https://github.com/jackvaughanjr/googleworkspace2snipe/actions/workflows/release.yml/badge.svg)](https://github.com/jackvaughanjr/googleworkspace2snipe/actions/workflows/release.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/jackvaughanjr/googleworkspace2snipe)](https://goreportcard.com/report/github.com/jackvaughanjr/googleworkspace2snipe) [![Downloads](https://img.shields.io/github/downloads/jackvaughanjr/googleworkspace2snipe/total)](https://github.com/jackvaughanjr/googleworkspace2snipe/releases)

Syncs Google Workspace license assignments into [Snipe-IT](https://snipeit.com/).
Each Google Workspace SKU (subscription) is created as a **separate Snipe-IT
license** — for example, "Google Workspace Business Plus" and "Google Vault" each
become their own license entry with per-user seat assignments.

Users are automatically checked out to the licenses they hold in Google Workspace
and checked back in when a license is removed or a user is suspended.

> **Note:** only licenses exposed by the [Google Enterprise License Manager API](https://developers.google.com/admin-sdk/licensing/overview)
> can be synced. Certain "Google Workspace add-on" subscription types (e.g. Google
> Voice Standard, AI Ultra Access) are not accessible through this API and cannot
> be synced.

Authentication uses a Google Cloud service account with
[domain-wide delegation](https://support.google.com/a/answer/162106) — no user
interaction required. Runs fully headless; suitable for cron or similar schedulers.

---

## Requirements

- A Google Cloud project with the **Enterprise License Manager API** enabled
- A service account with domain-wide delegation granted in the Google Admin Console
- A Snipe-IT instance with an API key that has license management permissions

---

## Google Cloud setup

1. **Enable the Enterprise License Manager API** (APIs & Services → Library →
   "Enterprise License Manager API").

2. **Enable the Admin SDK API** (only if you use OU filtering, enriched seat
   notes, or `--create-users`): in the same Library, search for "Admin SDK API"
   and enable it. If you do not use any of those features, skip this step.

3. **Create a service account** (IAM & Admin → Service Accounts → Create). No Cloud
   IAM roles are required. After creating it, go to Keys → Add Key → Create New Key
   → JSON and download the key file.

4. **Grant domain-wide delegation** in the [Google Admin Console](https://admin.google.com):
   - Security → Access and data control → API controls → Manage Domain Wide Delegation
     → Add new
   - **Client ID**: the service account's numeric Client ID (the `client_id` field in
     the JSON key)
   - **OAuth Scopes**: `https://www.googleapis.com/auth/apps.licensing`

5. **Grant the Directory API scope** (only if you use OU filtering, enriched seat
   notes, or `--create-users`): return to the same DWD entry created in step 4 and
   add a second scope, separated from the first by a comma:
   - **OAuth Scopes**: `https://www.googleapis.com/auth/apps.licensing,https://www.googleapis.com/auth/admin.directory.user.readonly`

   If you do not use `ou_paths`, `enrich_notes_for_skus`, or `--create-users`, this
   scope is never requested and does not need to be granted.

6. **Choose an admin email**: any Google Workspace super admin address in your domain.
   This is the account the service account will impersonate.

> **Note:** API enablements can take a few minutes to propagate. If you get an
> "API not enabled" error immediately after enabling, wait 2–3 minutes and retry.

---

## Installation

**Download a pre-built binary** from the [latest release](https://github.com/jackvaughanjr/googleworkspace2snipe/releases/latest):

```sh
# macOS (Apple Silicon)
curl -L https://github.com/jackvaughanjr/googleworkspace2snipe/releases/latest/download/googleworkspace2snipe-darwin-arm64 -o googleworkspace2snipe
chmod +x googleworkspace2snipe

# Linux (amd64)
curl -L https://github.com/jackvaughanjr/googleworkspace2snipe/releases/latest/download/googleworkspace2snipe-linux-amd64 -o googleworkspace2snipe
chmod +x googleworkspace2snipe

# Linux (arm64)
curl -L https://github.com/jackvaughanjr/googleworkspace2snipe/releases/latest/download/googleworkspace2snipe-linux-arm64 -o googleworkspace2snipe
chmod +x googleworkspace2snipe
```

Or build from source:

```sh
git clone https://github.com/jackvaughanjr/googleworkspace2snipe
cd googleworkspace2snipe
go build -o googleworkspace2snipe .
```

---

## Configuration

Copy `settings.example.yaml` to `settings.yaml` and fill in your values:

```sh
cp settings.example.yaml settings.yaml
$EDITOR settings.yaml
```

`settings.yaml` is gitignored and should never be committed.

### Minimal configuration

```yaml
google_workspace:
  credentials_file: "path/to/service-account.json"
  admin_email: "admin@your-domain.example.com"
  domain: "your-domain.example.com"

snipe_it:
  url: "https://your-snipe-it-instance.example.com"
  api_key: "your-snipe-it-api-key"
  license_category_id: 42   # required; find in Admin → Categories
```

### License naming (prefix / suffix)

By default, Snipe-IT license names match the Google SKU name exactly:

```
Google Workspace Business Plus
Google Vault
```

Use `license_name_prefix` and/or `license_name_suffix` to customize:

```yaml
google_workspace:
  license_name_prefix: ""               # e.g. "Acme - "
  license_name_suffix: " (acme.com)"    # e.g. for multi-tenant disambiguation
```

This produces:

```
Google Workspace Business Plus (acme.com)
Google Vault (acme.com)
```

The prefix and suffix are concatenated verbatim — include any desired separators
(spaces, dashes, parentheses) in the values themselves.

### OU filtering

To restrict the sync to users in specific Organizational Units (and their subtrees):

```yaml
google_workspace:
  ou_paths:
    - "/Engineering"
    - "/Sales"
```

With an OU filter active:
- **Checkout**: only users in the specified OUs are checked out to Snipe-IT licenses.
- **Checkin**: only seats belonging to users within the OU scope are checked in when
  those users lose a license. Seats for users outside the OU scope are left untouched,
  even if those users are currently checked out in Snipe-IT.

Leave `ou_paths` empty (or omit it) to sync all active users in the domain.

Requires the `admin.directory.user.readonly` DWD scope — see steps 2 and 5 of
the Google Cloud setup section above.

### Enriched seat notes

By default, each Snipe-IT seat's notes contain only stable product identifiers:

```
product_id: Google-Apps
sku_id: 1010020025
```

To also include per-user OU path and admin status for specific licenses, add
the SKU name or SKU ID to `enrich_notes_for_skus`:

```yaml
google_workspace:
  enrich_notes_for_skus:
    - "Google Workspace Business Plus"   # the license every user holds
```

Enriched notes look like:

```
product_id: Google-Apps
sku_id: 1010020025
org_unit: /Engineering
is_admin: false
```

`org_unit` and `is_admin` are updated automatically on subsequent syncs when a
user moves OUs or gains/loses admin privileges. Use `--force` to rewrite all
seat notes immediately — this applies to all licenses, not just enriched ones.

Also requires `admin.directory.user.readonly` — if both OU filtering and note
enrichment are configured, only one Directory API call is made per sync run.

### Automatic user creation

By default, Google Workspace license holders with no Snipe-IT account are skipped
with a warning and a Slack notification. Add `--create-users` to create the Snipe-IT
account automatically and proceed to check out the seat:

```sh
./googleworkspace2snipe sync --create-users
```

Or enable it permanently in `settings.yaml`:

```yaml
sync:
  create_users: true
```

Created accounts are configured so they do not cause problems:

- **Login disabled** (`activated: false`) — the user cannot sign into Snipe-IT
- **No welcome email** (`send_welcome: false`)
- **No group membership** — avoids any auto-assign license groups
- **Start date** set to the account's Google Workspace creation date
- **Notes** record the auto-creation source:
  `Auto-created from Google Workspace via googleworkspace2snipe`

Requires the `admin.directory.user.readonly` DWD scope — see step 5 of the Google
Cloud setup section above.

### Product IDs

By default, the following product families are queried:

- `Google-Apps` — Business / Enterprise / Education plans
- `Google-Vault` — Google Vault
- `Google-Drive-storage` — Additional storage

To add products not in this default list, run `discover` (see [Usage](#usage)) or
set `product_ids` manually:

```yaml
google_workspace:
  product_ids:
    - "Google-Apps"
    - "Google-Vault"
    - "Google-Drive-storage"
    - "101031"   # Google Workspace Migrate — example of a non-default product
```

Only SKUs with at least one active assignment produce a Snipe-IT license.

Not all subscriptions visible in the Google Admin Console are accessible via the
Enterprise License Manager API. "Google Workspace add-on" subscription types such
as Google Voice Standard and AI Ultra Access return `400 Invalid productId` from
the API and cannot be synced — this is an API-level limitation.

### Environment variable overrides

| Env var                   | Config key                          |
|---------------------------|-------------------------------------|
| `GOOGLE_CREDENTIALS_FILE` | `google_workspace.credentials_file` |
| `GOOGLE_ADMIN_EMAIL`      | `google_workspace.admin_email`      |
| `GOOGLE_DOMAIN`           | `google_workspace.domain`           |
| `SNIPE_URL`               | `snipe_it.url`                      |
| `SNIPE_TOKEN`             | `snipe_it.api_key`                  |
| `SLACK_WEBHOOK`           | `slack.webhook_url`                 |

---

## Usage

### Discover product IDs

```sh
./googleworkspace2snipe discover
```

Probes all known Google Workspace product IDs and writes the active ones back
into `settings.yaml` as `google_workspace.product_ids`. Run this once when
setting up a new domain or when you suspect you have add-ons outside the
built-in default list.

```
Probing 4 known Google Workspace product IDs...

  Google-Apps                               active
  Google-Vault                              not found
  Google-Drive-storage                      not found
  101031                                    not found

Active product IDs (1):
  - Google-Apps

Updated google_workspace.product_ids in settings.yaml
```

Use `--dry-run` to print the discovered list without modifying `settings.yaml`.

### Validate connections

```sh
./googleworkspace2snipe test
```

Lists all Google Workspace SKUs with active assignments and shows whether the
corresponding Snipe-IT license already exists:

```
=== Google Workspace ===
Domain:   acme.com
Products: [Google-Apps Google-Vault Google-Drive-storage]

=== SKUs → Snipe-IT Licenses ===
Snipe-IT License Name                               Users  Snipe-IT Status
------------------------------------------------------------------------------------------
Google Workspace Business Plus                         67  id=38 seats=70 free=3
```

### Run a sync

```sh
# Dry run — see what would change without making any changes
./googleworkspace2snipe sync --dry-run

# Real sync
./googleworkspace2snipe sync

# Sync all licenses for a single user
./googleworkspace2snipe sync --email user@your-domain.example.com

# Force re-sync of all seat notes (even if unchanged)
./googleworkspace2snipe sync --force

# Create Snipe-IT accounts for users not yet in Snipe-IT, then check them out
./googleworkspace2snipe sync --create-users
```

### Global flags

| Flag              | Description                                 |
|-------------------|---------------------------------------------|
| `--config PATH`   | Path to config file (default: settings.yaml)|
| `-v, --verbose`   | INFO-level logging                          |
| `-d, --debug`     | DEBUG-level logging                         |
| `--log-file PATH` | Append logs to a file                       |
| `--log-format`    | `text` (default) or `json`                  |
| `--version`       | Print version and exit                      |

---

## Snipe-IT seat notes

Each seat's `notes` field contains stable identifiers for debugging:

```
product_id: Google-Apps
sku_id: 1010020025
```

These values are written once on checkout and do not change, so subsequent syncs
show seats as `skipped`. Use `--force` to rewrite all notes.

---

## Slack notifications

Set `slack.webhook_url` in `settings.yaml` (or `SLACK_WEBHOOK` env var) to
receive notifications for:

- Sync failures
- Google Workspace users with no matching Snipe-IT account, deduplicated across
  licenses (only when `--create-users` is not set; successful creations do not
  trigger this notification)
- Sync completion summaries

All notifications are suppressed in `--dry-run` mode.

---

## Releases

## Version History

| Version | Key changes |
|---------|-------------|
| v1.0.0 | Initial scaffold — Google Workspace → Snipe-IT license seat sync |
| v1.1.0 | Redesign — sync each Google Workspace SKU as a separate Snipe-IT license (was one combined license) |
| v1.1.1 | Docs: added TODO for discover command |
| v1.1.2 | Docs: added TODO for `--create-users` flag |
| v1.2.0 | Added OU filtering and per-license note enrichment |
| v1.2.1 | Simplified array config fields; improved JSON credentials documentation |
| v1.2.2 | Surfaced Directory API scope setup steps in docs |
| v1.2.3 | Added Admin SDK API enablement step for Directory API features |
| v1.3.0 | Validate Google API access before sync with actionable error messages |
| v1.3.1 | Fixed erroneous usage block and duplicate error echo for runtime errors |
| v1.3.2 | Fixed `ValidateAPIs` running before `GetUserMap` in test command |
| v1.3.3 | Filled gaps in README and CONTEXT.md identified by audit |
| v1.4.0 | Added `discover` command to auto-detect Google Workspace product IDs; fixed 400 for add-on products |
| v1.5.0 | Added `--create-users` flag to automatically provision missing Snipe-IT accounts |

Releases are built automatically by GitHub Actions on `v*` tag push:

```sh
git tag v1.0.0
git push origin v1.0.0
```

Four platform binaries are attached to each release:
`darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64.exe`.
