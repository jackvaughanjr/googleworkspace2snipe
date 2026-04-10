# CONTEXT.md — googleworkspace2snipe

This file documents everything specific to the Google Workspace integration.
Cross-cutting conventions live in `CLAUDE.md`.

---

## Purpose

Syncs Google Workspace license assignments into Snipe-IT. Each Google Workspace
SKU (subscription) is created as a **separate Snipe-IT license** — for example,
"Google Workspace Business Plus" and "Google Vault" each become their own
Snipe-IT license entry. Users are checked out to the specific licenses they hold,
and checked back in when a license is removed.

**API scope**: only licenses exposed by the Google Enterprise License Manager API
can be synced. Certain "Google Workspace add-on" subscription types (e.g. Google
Voice Standard, AI Ultra Access) return `400 Invalid productId` from this API and
are not accessible — see gotcha #9 below.

---

## Auth method

**Service account with domain-wide delegation (DWD).**

The integration reads a Google service account JSON key file and impersonates a
Google Workspace super admin via domain-wide delegation. No user interaction is
required — it runs fully headless.

### Required OAuth 2.0 scopes

The base scope is always required:

```
https://www.googleapis.com/auth/apps.licensing
```

The Directory API scope is additionally required when any of the following are
configured or enabled:
- `google_workspace.ou_paths`
- `google_workspace.enrich_notes_for_skus`
- `--create-users` / `sync.create_users: true`

If none of these features are used, this scope is **not included in the JWT
`scope` claim** and does not need to be granted in DWD.

```
https://www.googleapis.com/auth/admin.directory.user.readonly
```

Both scopes can coexist on the same DWD entry — enter them as a
comma-separated list in the Admin Console. The scope is determined at client
construction time (`NewClientFromFile(..., withDirectory bool)`) and cannot
change during a run.

### Setup steps

1. **Create or choose a Google Cloud project** where your service account will live.

2. **Enable the Enterprise License Manager API**:
   - Go to APIs & Services → Library in the Cloud Console.
   - Search for "Enterprise License Manager API" and enable it.

3. **Enable the Admin SDK API** (only if using `ou_paths`, `enrich_notes_for_skus`,
   or `--create-users`):
   - In the same Library, search for "Admin SDK API" and enable it.
   - API enablements can take a few minutes to propagate.

4. **Create a service account**:
   - Go to IAM & Admin → Service Accounts → Create Service Account.
   - Name it (e.g. `snipe-sync`). No Cloud IAM roles are required.
   - After creating it, go to Keys → Add Key → Create New Key → JSON.
   - Download the JSON file. **This is your `credentials_file`.**

5. **Grant domain-wide delegation** in the Google Admin Console
   (admin.google.com, not Cloud Console):
   - Go to Security → Access and data control → API controls →
     Manage Domain Wide Delegation → Add new.
   - Enter:
     - **Client ID**: the service account's numeric client ID (the `client_id`
       field in the JSON key file, or shown as "Unique ID" in the Cloud Console).
     - **OAuth Scopes**: `https://www.googleapis.com/auth/apps.licensing`
   - If you plan to use `ou_paths`, `enrich_notes_for_skus`, or `--create-users`,
     edit the same DWD entry and add the Directory API scope as a second
     comma-separated value:
     `https://www.googleapis.com/auth/apps.licensing,https://www.googleapis.com/auth/admin.directory.user.readonly`

6. **Choose an admin email** (`google_workspace.admin_email`): any super admin
   address in the domain. The service account impersonates this account. The
   account must be a super admin, not just a delegated admin.

---

## API details

- **API**: Google Enterprise License Manager API (not the Directory API)
- **Base URL**: `https://licensing.googleapis.com/apps/licensing/v1`
- **Endpoint**: `GET /product/{productId}/users?customerId={domain}&maxResults=1000`
- **Pagination**: uses `nextPageToken` in the response body; followed until empty.
- **Response**: returns `LicenseAssignment` objects with `userId` (email), `productId`,
  `skuId`, `skuName`, and `productName` for each active assignment.
- **SKU grouping**: the client groups all assignments by (productId, skuId) to
  produce one `SkuGroup` per distinct SKU. Each `SkuGroup` maps to one Snipe-IT license.

### Product IDs

The following product IDs are queried by default when `product_ids` is not set:

| Product ID              | Products covered                                     |
|-------------------------|------------------------------------------------------|
| `Google-Apps`           | Google Workspace Business / Enterprise / Education   |
| `Google-Vault`          | Google Vault                                         |
| `Google-Drive-storage`  | Google additional storage                            |

Use `discover` to probe additional known product IDs (`101031` — Google Workspace
Migrate — is also in the probed list) and write active ones to `settings.yaml`.

**Important**: not all subscriptions visible in the Google Admin Console are
accessible via the Enterprise License Manager API. "Google Workspace add-on" type
subscriptions (e.g. Google Voice Standard, AI Ultra Access) return
`400 Invalid productId` from the API and cannot be synced — there is no workaround
at the API level. Only products that return HTTP 200 from the Licensing API can be
managed by this tool.

---

## Config schema

### settings.yaml keys

```yaml
google_workspace:
  credentials_file: "path/to/service-account.json"
  admin_email: "admin@your-domain.example.com"
  domain: "your-domain.example.com"
  product_ids: []                  # optional; empty = DefaultProductIDs
  license_name_prefix: ""          # e.g. "Acme - "
  license_name_suffix: ""          # e.g. " (acme.com)"
  ou_paths: []                     # optional; restricts checkout+checkin scope
  enrich_notes_for_skus: []        # optional; SKU names or IDs for rich notes

snipe_it:
  url: "https://snipe.your-domain.example.com"
  api_key: ""
  license_category_id: 0           # required
  license_manufacturer_id: 0       # optional; 0 = auto find/create "Google"
  license_supplier_id: 0           # optional

sync:
  dry_run: false
  force: false
  create_users: false

slack:
  webhook_url: ""
```

### Environment variable overrides

| Env var                   | Config key                            |
|---------------------------|---------------------------------------|
| `GOOGLE_CREDENTIALS_FILE` | `google_workspace.credentials_file`   |
| `GOOGLE_ADMIN_EMAIL`      | `google_workspace.admin_email`        |
| `GOOGLE_DOMAIN`           | `google_workspace.domain`             |
| `SNIPE_URL`               | `snipe_it.url`                        |
| `SNIPE_TOKEN`             | `snipe_it.api_key`                    |
| `SLACK_WEBHOOK`           | `slack.webhook_url`                   |

List-type config keys (`product_ids`, `ou_paths`, `enrich_notes_for_skus`) and
string formatting keys (`license_name_prefix`, `license_name_suffix`) cannot be
overridden via env vars; set them in `settings.yaml`.

---

## License naming

The Snipe-IT license name for each SKU is:

```
{license_name_prefix}{Google SKU name}{license_name_suffix}
```

Prefix and suffix are concatenated verbatim — include any separator characters
(spaces, dashes, parentheses) in the prefix/suffix values themselves.

**Examples:**

| SKU Name                       | Prefix       | Suffix          | Snipe-IT License Name                          |
|--------------------------------|--------------|-----------------|------------------------------------------------|
| Google Workspace Business Plus | *(empty)*    | *(empty)*       | `Google Workspace Business Plus`               |
| Google Vault                   | `"Acme - "`  | *(empty)*       | `Acme - Google Vault`                          |
| Google Workspace Business Plus | *(empty)*    | `" (acme.com)"` | `Google Workspace Business Plus (acme.com)`    |

---

## OU filtering

Set `google_workspace.ou_paths` to restrict the sync to users in specific
Organizational Units and their subtrees:

```yaml
google_workspace:
  ou_paths:
    - "/Engineering"
    - "/Sales"
```

Behaviour with OU filter active:
- **Checkout pass**: only users within the specified OUs are checked out to Snipe-IT licenses.
- **Checkin pass**: only seats belonging to users within the OU scope are checked in when
  those users lose a license. Seats for users outside the OU scope are left untouched,
  even if they are checked out in Snipe-IT.
- **Seat counts**: license seat expansion uses the count of in-scope users, not the
  global user count.
- **`test` output**: an "In Scope" column shows the per-SKU user count after OU filtering,
  alongside the global user count.

Requires the `admin.directory.user.readonly` DWD scope. The Directory API is called once
per sync run; results are cached for the duration of the run.

---

## Note enrichment

Set `google_workspace.enrich_notes_for_skus` to include per-user OU path and admin
status in the Snipe-IT seat notes for specific licenses:

```yaml
google_workspace:
  enrich_notes_for_skus:
    - "Google Workspace Business Plus"   # match by SKU name (case-insensitive)
    - "1010020020"                        # or match by SKU ID
```

The primary Workspace license that all users hold is a natural candidate, since its
notes then reflect each user's current department and admin status and update
automatically when users move OUs or gain/lose admin privileges.

Requires the `admin.directory.user.readonly` DWD scope (same as OU filtering — if
both are configured, only one Directory API call is made).

---

## Automatic user creation

By default, if a Google Workspace license holder has no Snipe-IT account, the sync
warns, skips them, and sends a Slack notification. With `--create-users` (or
`sync.create_users: true` in `settings.yaml`), the sync creates the Snipe-IT account
automatically and then proceeds to check out the seat.

### Created user properties

| Field          | Value                                                              |
|----------------|--------------------------------------------------------------------|
| `first_name`   | Given name from the Google Directory API                           |
| `last_name`    | Family name from the Google Directory API                          |
| `email`        | Google Workspace primary email                                     |
| `username`     | Same as email (globally unique within Snipe-IT)                    |
| `password`     | Cryptographically random 32-hex string (user cannot log in anyway) |
| `activated`    | `false` — user cannot log into Snipe-IT                           |
| `send_welcome` | `false` — no welcome email is sent                                 |
| `start_date`   | Account creation date from Google Workspace (`YYYY-MM-DD`)         |
| `notes`        | `"Auto-created from Google Workspace via googleworkspace2snipe"`   |
| Groups         | None — avoids any auto-assign license groups                       |

### Fallback name derivation

When a user's Directory API entry is not available (e.g. the user is outside the
OU filter scope but still holds a license), the first and last name are derived
from the email local-part by splitting on `.`:

- `jane.doe@example.com` → first: `jane`, last: `doe`
- `jdoe@example.com` → first: `jdoe`, last: *(empty)*

### Required DWD scope

`--create-users` requires the Directory API scope — the same scope used by OU
filtering and note enrichment. If neither of those features is configured, add
the scope to the DWD entry specifically for user creation:

```
https://www.googleapis.com/auth/apps.licensing,https://www.googleapis.com/auth/admin.directory.user.readonly
```

### Dry-run behaviour

With `--dry-run --create-users`, creation is simulated: the sync logs
`[dry-run] would create Snipe-IT user` and increments both `users_created` and
`checked_out` counters without making any API calls.

### Result counters

`users_created` is reported in the console output line and the Slack completion
message alongside the other counters.

---

## Seat notes format

**Standard notes** (all SKUs unless configured for enrichment):

```
product_id: Google-Apps
sku_id: 1010020025
```

These values are stable identifiers that never change for a given license. Notes are
written once on checkout and not updated on subsequent syncs (seats show as `skipped`).

**Enriched notes** (SKUs listed in `enrich_notes_for_skus`):

```
product_id: Google-Apps
sku_id: 1010020025
org_unit: /Engineering
is_admin: false
```

`org_unit` and `is_admin` reflect the user's current state at sync time. When a user
moves OUs or their admin status changes, the notes are updated on the next sync (the
`notes_updated` counter increments). Use `--force` to rewrite all enriched notes
immediately regardless of current state.

---

## Google-specific gotchas

1. **Token exchange, not OAuth flow.** The service account JWT is exchanged for a
   short-lived access token at the token URI in the credentials file. Tokens expire
   after 1 hour; the client caches and refreshes automatically with a 30-second buffer.

2. **`sub` claim = admin email, not service account email.** The JWT's `sub` claim
   must be the Google Workspace admin being impersonated, not the service account's
   own `client_email`. This is the most common DWD misconfiguration.

3. **Client ID vs client email.** DWD is granted using the service account's numeric
   **Client ID** (the `client_id` field in the JSON key, shown as "Unique ID" in the
   Cloud Console). The client email (`...@...iam.gserviceaccount.com`) is used in the
   JWT `iss` claim but is not what you enter in the Admin Console.

4. **Scope mismatch.** If you previously granted the Directory scope
   (`admin.directory.user.readonly`) for an earlier version of this integration,
   you must also add the licensing scope (`apps.licensing`) in the Admin Console
   DWD page. Both scopes can coexist on the same service account entry.

5. **404 and 400 on product IDs.** If a product ID is not provisioned for the
   domain the API returns 404. If the product ID is not a valid Licensing API
   product (common for "Google Workspace add-on" subscription types), the API
   returns `400 Invalid productId`. The client treats both as "no assignments"
   rather than a fatal error, so listing unknown product IDs in `product_ids` is
   harmless — they are silently skipped.

6. **Pagination is required.** The API returns at most 1000 assignments per page.
   `nextPageToken` is followed until empty.

7. **Zero-assignment SKUs are invisible.** If all users of a SKU lose that license,
   it no longer appears in API results. Snipe-IT seats that were previously checked
   out will remain checked out until at least one user holds the SKU again (triggering
   the checkin pass) or an admin manually checks them in. This is a known limitation
   of the License Manager API — there is no endpoint to enumerate SKUs with zero
   assignments.

8. **`userId` is the primary email.** The `userId` field in license assignment
   responses is the user's primary email address (not a numeric ID), even though
   the field name implies otherwise. Matching to Snipe-IT users is done by
   lowercased email.

9. **Add-on product IDs.** Many "Google Workspace add-on" subscriptions visible in
   the Admin Console (e.g. Google Voice Standard, AI Ultra Access) are **not
   accessible via the Enterprise License Manager API** — they return
   `400 Invalid productId` regardless of the product ID tried. This is an API-level
   limitation with no known workaround. Use `discover` to identify which product IDs
   actually return data for your domain; only those can be synced.

10. **API validation runs before every sync.** Both `test` and `sync` call
    `ValidateAPIs()` before doing any real work. It makes a minimal probe request
    to each configured API and maps the response to a specific error:
    - **403 "has not been used" / "disabled"** → API not enabled in GCP project.
      Error message names the API and links to where to enable it.
    - **403 other** → API is enabled but the DWD scope is not granted in the Admin
      Console. Error message points to Security → API controls → Domain-wide Delegation.
    - **200 or 404** → API is reachable (success; 404 just means no data for that probe).
    The Directory API is only probed when `withDirectory=true` was passed at
    construction — i.e., when `ou_paths`, `enrich_notes_for_skus`, or
    `sync.create_users` is configured.

---

## File structure

```
main.go
cmd/
  root.go        # cobra root, viper init, logging
  discover.go    # discover command: probe product IDs, write to settings.yaml
  sync.go        # sync command
  test.go        # test command
internal/
  googleworkspace/
    client.go    # License Manager API client with service account DWD auth
  slack/
    client.go    # Slack webhook client (verbatim from CLAUDE.md)
  snipeit/
    client.go    # Snipe-IT API client (verbatim from CLAUDE.md)
  sync/
    syncer.go    # per-SKU sync logic
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
