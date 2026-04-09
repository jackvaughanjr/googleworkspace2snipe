# CONTEXT.md — googleworkspace2snipe

This file documents everything specific to the Google Workspace integration.
Cross-cutting conventions live in `CLAUDE.md`.

---

## Purpose

Syncs Google Workspace license assignments into Snipe-IT. Each Google Workspace
SKU (subscription) is created as a **separate Snipe-IT license** — for example,
"Google Workspace Business Plus", "Google Voice Standard", and "AI Ultra Access"
each become their own Snipe-IT license entry. Users are checked out to the
specific licenses they hold, and checked back in when a license is removed.

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

The Directory API scope is additionally required when either
`google_workspace.ou_paths` or `google_workspace.enrich_notes_for_skus` is
configured. If neither feature is used, this scope is not requested and does
not need to be granted in DWD.

```
https://www.googleapis.com/auth/admin.directory.user.readonly
```

Both scopes can coexist on the same DWD entry — enter them as a
comma-separated list in the Admin Console.

### Setup steps

1. **Create or choose a Google Cloud project** where your service account will live.

2. **Enable the Enterprise License Manager API**:
   - Go to APIs & Services → Library in the Cloud Console.
   - Search for "Enterprise License Manager API" and enable it.

3. **Create a service account**:
   - Go to IAM & Admin → Service Accounts → Create Service Account.
   - Name it (e.g. `snipe-sync`). No Cloud IAM roles are required.
   - After creating it, go to Keys → Add Key → Create New Key → JSON.
   - Download the JSON file. **This is your `credentials_file`.**

4. **Grant domain-wide delegation** in the Google Admin Console
   (admin.google.com, not Cloud Console):
   - Go to Security → Access and data control → API controls →
     Manage Domain Wide Delegation → Add new.
   - Enter:
     - **Client ID**: the service account's numeric client ID (the `client_id`
       field in the JSON key file, or shown as "Unique ID" in the Cloud Console).
     - **OAuth Scopes**: `https://www.googleapis.com/auth/apps.licensing`
   - If you plan to use `ou_paths` or `enrich_notes_for_skus`, edit the same
     DWD entry and add the Directory API scope as a second comma-separated value:
     `https://www.googleapis.com/auth/apps.licensing,https://www.googleapis.com/auth/admin.directory.user.readonly`

5. **Choose an admin email** (`google_workspace.admin_email`): any super admin
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
| `Cloud-Identity`        | Cloud Identity Free / Premium                        |
| `Google-Voice`          | Google Voice Starter / Standard / Premier            |

Add additional product IDs to `google_workspace.product_ids` in `settings.yaml`
if your domain uses products not covered by this list. The full list is documented
at https://developers.google.com/admin-sdk/licensing/v1/how-tos/products.

**Note on add-ons** (e.g. AI Ultra Access, Gemini): newer Google Workspace add-ons
may use product IDs not in the default list. If a subscription appears in the
Google Admin Console but not in `test` output, add its product ID to
`google_workspace.product_ids`. You can find product IDs via the Google Admin
SDK API Explorer or by inspecting network traffic in the Admin Console.

<!-- TODO: implement a --create-users flag (and sync.create_users setting) that
automatically creates a Snipe-IT user for any Google Workspace license holder
not found in Snipe-IT, rather than warning and skipping them. Without the flag,
current behaviour (warn + skip + Slack notification) is preserved.
Implementation notes:
- Requires the Directory API scope (admin.directory.user.readonly) in addition
  to apps.licensing, so user display name and department can be populated on
  the new Snipe-IT account.
- Snipe-IT user creation uses POST /api/v1/users with at minimum: first_name,
  last_name, username (email), email, password (random or forced-reset).
- Add CreateUser to internal/snipeit/client.go; add the scope to the JWT
  and update CONTEXT.md DWD setup instructions accordingly.
- The Directory API client method (e.g. GetUser(ctx, email)) can reuse the
  existing JWT auth infrastructure — just add the second scope space-separated
  in the buildJWT scope claim and add the Directory API base URL constant. -->

<!-- TODO: implement a `discover` command that connects to the configured Google
Workspace, enumerates all product IDs that have at least one active license
assignment (by iterating the known master product list from Google's docs, or
via a future API endpoint if one becomes available), and writes the discovered
product_ids list back into settings.yaml automatically. This solves the add-on
discovery problem without requiring the user to research product IDs manually.
Implementation note: the License Manager API has no "list all products for a
customer" endpoint as of 2026-04; discovery must be done by querying each known
product ID and collecting non-404 responses. The full known list is at
https://developers.google.com/admin-sdk/licensing/v1/how-tos/products -->

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

| SKU Name                      | Prefix       | Suffix        | Snipe-IT License Name                              |
|-------------------------------|--------------|---------------|----------------------------------------------------|
| Google Workspace Business Plus | *(empty)*    | *(empty)*     | `Google Workspace Business Plus`                   |
| Google Voice Standard          | `"Acme - "`  | *(empty)*     | `Acme - Google Voice Standard`                     |
| Google Workspace Business Plus | *(empty)*    | `" (acme.com)"` | `Google Workspace Business Plus (acme.com)`     |

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

5. **404 on product IDs.** If a product ID is not provisioned for the domain, the
   API returns 404. The client treats this as "no assignments" rather than an error,
   so listing unused products in `product_ids` is harmless.

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

9. **Add-on product IDs.** Newer add-ons (AI Ultra Access, Gemini, etc.) may have
   product IDs outside the default list. Use `test` to verify all expected SKUs
   appear, and add missing product IDs to `google_workspace.product_ids`.

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
