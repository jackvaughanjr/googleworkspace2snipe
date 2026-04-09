# CONTEXT.md — googleworkspace2snipe

This file documents everything specific to the Google Workspace integration.
Cross-cutting conventions live in `CLAUDE.md`.

---

## Purpose

Syncs active Google Workspace users (non-suspended, non-archived) into Snipe-IT
as license seat assignments. Each active user is checked out a seat on a named
Snipe-IT license (default: `"Google Workspace"`). Users who are suspended,
archived, or removed are checked back in automatically.

---

## Auth method

**Service account with domain-wide delegation (DWD).**

The integration reads a Google service account JSON key file and impersonates a
Google Workspace super admin via domain-wide delegation. No user interaction is
required — it runs fully headless.

### Required OAuth 2.0 scope

```
https://www.googleapis.com/auth/admin.directory.user.readonly
```

### Setup steps

1. **Create or choose a Google Cloud project** where your service account will live.

2. **Enable the Admin SDK API**:
   - Go to APIs & Services → Library in the Cloud Console.
   - Search for "Admin SDK API" and enable it.

3. **Create a service account**:
   - Go to IAM & Admin → Service Accounts → Create Service Account.
   - Name it (e.g. `snipe-sync`). No Cloud IAM roles are required.
   - After creating it, go to Keys → Add Key → Create New Key → JSON.
   - Download the JSON file. **This is your `credentials_file`.**

4. **Grant domain-wide delegation** in the Google Admin Console
   (admin.google.com, not Cloud Console):
   - Go to Security → Access and data control → API controls →
     Manage Domain Wide Delegation.
   - Click "Add new" and enter:
     - **Client ID**: the service account's numeric client ID (found in the JSON
       key as `client_id`, or in the Cloud Console under the service account).
     - **OAuth Scopes**: `https://www.googleapis.com/auth/admin.directory.user.readonly`

5. **Choose an admin email** (`google_workspace.admin_email`): any super admin
   address in the domain. The service account impersonates this account to call
   the Directory API. The account must be a super admin, not just a delegated
   admin.

---

## API details

- **Base URL**: `https://admin.googleapis.com/admin/directory/v1`
- **Users endpoint**: `GET /users?domain={domain}&maxResults=500&query=isSuspended=false`
- **Pagination**: uses `nextPageToken` in the response body; follow until empty.
- **OU filter**: set `orgUnitPath=/Path/To/OU`; returns users in that OU and all
  sub-OUs. Multiple OUs are fetched in separate requests; duplicates are
  deduplicated by user ID.
- **Active user filter**: `isSuspended=false` in the query parameter excludes
  suspended users. The `archived` field is checked in code as a secondary filter.

---

## Config schema

### settings.yaml keys

```yaml
google_workspace:
  credentials_file: "path/to/service-account.json"
  admin_email: "admin@your-domain.example.com"
  domain: "your-domain.example.com"
  ou_paths: []   # optional; empty = all users

snipe_it:
  url: "https://snipe.your-domain.example.com"
  api_key: ""
  license_name: "Google Workspace"
  license_category_id: 0     # required
  license_manufacturer_id: 0  # optional; 0 = auto find/create "Google"
  license_supplier_id: 0      # optional

sync:
  dry_run: false
  force: false

slack:
  webhook_url: ""
```

### Environment variable overrides

| Env var                  | Config key                            |
|--------------------------|---------------------------------------|
| `GOOGLE_CREDENTIALS_FILE`| `google_workspace.credentials_file`   |
| `GOOGLE_ADMIN_EMAIL`     | `google_workspace.admin_email`        |
| `GOOGLE_DOMAIN`          | `google_workspace.domain`             |
| `SNIPE_URL`              | `snipe_it.url`                        |
| `SNIPE_TOKEN`            | `snipe_it.api_key`                    |
| `SLACK_WEBHOOK`          | `slack.webhook_url`                   |

`ou_paths` cannot be overridden via a single env var (it is a list); set it in
`settings.yaml`.

---

## Seat notes format

The `notes` field written to each Snipe-IT seat contains:

```
org_unit: /Engineering
is_admin: false
```

`org_unit` reflects the user's current Google Workspace organizational unit path.
`is_admin` is `true` for super admins.

---

## Google-specific gotchas

1. **Token exchange, not OAuth flow.** The service account JWT is exchanged for a
   short-lived access token at `https://oauth2.googleapis.com/token`. Tokens
   expire after 1 hour; the client caches and refreshes automatically with a
   30-second buffer.

2. **`sub` claim = admin email, not service account email.** The JWT's `sub` claim
   must be the Google Workspace admin being impersonated, not the service account's
   own email (`client_email`). This is the most common DWD misconfiguration.

3. **Client ID vs client email.** DWD is granted using the service account's
   numeric **Client ID** (the `client_id` field in the JSON key, also shown as
   "Unique ID" in the Cloud Console). The client email (`...@...iam.gserviceaccount.com`)
   is used in the JWT `iss` claim but is not what you enter in the Admin Console.

4. **Pagination is required.** The API returns at most 500 users per page.
   `nextPageToken` must be followed until empty or users will be silently missed.

5. **Suspended ≠ deleted.** `isSuspended=false` in the query excludes suspended
   users. Archived users (Google Workspace for Education) have `archived: true`
   in the response body; these are filtered in code. Deleted users do not appear
   in the default list response.

6. **OU path format.** Must start with `/` (e.g. `/Engineering`, not `Engineering`).
   The root OU is `/`.

7. **Duplicate users across OUs.** When multiple `ou_paths` are configured, a user
   can theoretically appear in results for two OUs (if OU paths overlap). The
   client deduplicates by user ID before returning.

8. **Admin SDK quota.** The Directory API has a default quota of 1,500 requests
   per 100 seconds per user. At typical org sizes (< 50,000 users), a single
   paginated list call is 1–100 requests and will not approach this limit.

---

## File structure

```
main.go
cmd/
  root.go        # cobra root, viper init, logging
  sync.go        # sync command
  test.go        # test command
internal/
  googleworkspace/
    client.go    # Admin SDK client with service account DWD auth
  slack/
    client.go    # Slack webhook client (verbatim from CLAUDE.md)
  snipeit/
    client.go    # Snipe-IT API client (verbatim from CLAUDE.md)
  sync/
    syncer.go    # core sync logic
    result.go    # Result struct
.github/
  workflows/
    release.yml
go.mod
go.sum
settings.example.yaml
README.md
CONTEXT.md
.gitignore
```
