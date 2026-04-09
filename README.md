# googleworkspace2snipe

Syncs active Google Workspace users into [Snipe-IT](https://snipeit.com/) as
license seat assignments. Each active user (non-suspended, non-archived) is
checked out a seat on a named Snipe-IT license. Users who are later suspended,
archived, or removed are automatically checked back in.

Authentication uses a Google Cloud service account with
[domain-wide delegation](https://support.google.com/a/answer/162106) — no user
interaction required. The binary runs fully headless and is suitable for
scheduling via cron or a similar scheduler.

---

## Requirements

- A Google Cloud project with the Admin SDK API enabled
- A service account with domain-wide delegation granted in the Google Admin Console
- A Snipe-IT instance with an API key that has license management permissions

---

## Google Cloud setup

1. **Enable the Admin SDK API** in your Google Cloud project (APIs & Services → Library → "Admin SDK API").

2. **Create a service account** (IAM & Admin → Service Accounts → Create). No Cloud IAM roles are required. After creating it, go to Keys → Add Key → Create New Key → JSON and download the key file.

3. **Grant domain-wide delegation** in the [Google Admin Console](https://admin.google.com):
   - Security → Access and data control → API controls → Manage Domain Wide Delegation → Add new
   - **Client ID**: the service account's numeric Client ID (the `client_id` field in the JSON key)
   - **OAuth Scopes**: `https://www.googleapis.com/auth/admin.directory.user.readonly`

4. **Choose an admin email**: any Google Workspace super admin address in your domain. This is the account the service account will impersonate.

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

### OU filtering

To sync only users in specific Organizational Units (and their subtrees):

```yaml
google_workspace:
  ou_paths:
    - "/Engineering"
    - "/Sales"
```

Leave `ou_paths` empty (or omit it) to sync all active users in the domain.

### Environment variable overrides

All sensitive values can be provided via environment variables instead of the
config file:

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

Reports the number of active users in Google Workspace and the current state
of the Snipe-IT license (if it exists).

### Run a sync

```sh
# Dry run — no changes made
./googleworkspace2snipe sync --dry-run

# Real sync
./googleworkspace2snipe sync

# Sync a single user
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

Each seat's `notes` field is set to:

```
org_unit: /Engineering
is_admin: false
```

Notes are updated automatically when a user moves OUs or their admin status
changes. Use `--force` to re-sync all notes regardless of current state.

---

## Slack notifications

Set `slack.webhook_url` in `settings.yaml` (or `SLACK_WEBHOOK` env var) to
receive notifications for:

- Sync failures
- Google Workspace users with no matching Snipe-IT account
- Sync completion summaries

All notifications are suppressed in `--dry-run` mode.

---

## Releases

Releases are built automatically by GitHub Actions on `v*` tag push:

```sh
git tag v1.0.0
git push origin v1.0.0
```

Four platform binaries are attached to each release:
`darwin-arm64`, `linux-amd64`, `linux-arm64`, `windows-amd64.exe`.
