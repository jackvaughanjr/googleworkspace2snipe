# googleworkspace2snipe

Syncs Google Workspace license assignments into [Snipe-IT](https://snipeit.com/).
Each Google Workspace SKU (subscription) is created as a **separate Snipe-IT
license** — for example, "Google Workspace Business Plus", "Google Voice Standard",
and "AI Ultra Access" each become their own license entry with per-user seat
assignments.

Users are automatically checked out to the licenses they hold in Google Workspace
and checked back in when a license is removed or a user is suspended.

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

2. **Create a service account** (IAM & Admin → Service Accounts → Create). No Cloud
   IAM roles are required. After creating it, go to Keys → Add Key → Create New Key
   → JSON and download the key file.

3. **Grant domain-wide delegation** in the [Google Admin Console](https://admin.google.com):
   - Security → Access and data control → API controls → Manage Domain Wide Delegation
     → Add new
   - **Client ID**: the service account's numeric Client ID (the `client_id` field in
     the JSON key)
   - **OAuth Scopes**: `https://www.googleapis.com/auth/apps.licensing`

4. **Choose an admin email**: any Google Workspace super admin address in your domain.
   This is the account the service account will impersonate.

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
Google Voice Standard
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
Google Voice Standard (acme.com)
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

With an OU filter active, only users in those OUs are checked out or checked in.
Seats belonging to users outside the filter are left untouched. Leave `ou_paths`
empty (or omit it) to sync all active users in the domain.

Requires the `admin.directory.user.readonly` DWD scope to be added alongside
`apps.licensing` in the Admin Console (see CONTEXT.md).

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
enriched notes immediately.

Also requires `admin.directory.user.readonly` — if both OU filtering and note
enrichment are configured, only one Directory API call is made per sync run.

### Product IDs

By default, the following product families are queried:

- `Google-Apps` — Business / Enterprise / Education plans
- `Google-Vault` — Google Vault
- `Google-Drive-storage` — Additional storage
- `Cloud-Identity` — Cloud Identity
- `Google-Voice` — Google Voice

To add products not in this default list (such as newer add-ons):

```yaml
google_workspace:
  product_ids:
    - "Google-Apps"
    - "Google-Vault"
    - "Google-Drive-storage"
    - "Cloud-Identity"
    - "Google-Voice"
    - "YOUR-ADDON-PRODUCT-ID"   # add extras here
```

Only SKUs with at least one active assignment produce a Snipe-IT license.

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

### Validate connections

```sh
./googleworkspace2snipe test
```

Lists all Google Workspace SKUs with active assignments and shows whether the
corresponding Snipe-IT license already exists:

```
=== Google Workspace ===
Domain:   acme.com
Products: [Google-Apps Google-Vault ...]

=== SKUs → Snipe-IT Licenses ===
Snipe-IT License Name                               Users  Snipe-IT Status
------------------------------------------------------------------------------------------
Google Voice Standard                                   3  id=42 seats=3 free=0
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
- Google Workspace users with no matching Snipe-IT account (deduplicated — one
  message per user even if they hold multiple licenses)
- Sync completion summaries

All notifications are suppressed in `--dry-run` mode.

---

## Roadmap

<!-- TODO: add a --create-users flag (and sync.create_users setting) that automatically
creates a Snipe-IT user account for any Google Workspace license holder who does not
already exist in Snipe-IT, instead of warning and skipping them. The created user
should be populated from the license assignment data available at sync time (email,
name if accessible). Without this flag the current behaviour (warn + skip + notify
via Slack) is preserved. -->

<!-- TODO: add a `discover` command that connects to the configured Google Workspace,
enumerates all product IDs with at least one active license assignment, and
writes the discovered product_ids list back into settings.yaml automatically.
This removes the need to manually research and maintain product IDs for add-ons
and newer Google Workspace products that fall outside the built-in default list. -->

---

## Releases

Releases are built automatically by GitHub Actions on `v*` tag push:

```sh
git tag v1.0.0
git push origin v1.0.0
```

Four platform binaries are attached to each release:
`darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64.exe`.
