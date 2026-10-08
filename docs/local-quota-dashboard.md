# Local quota dashboard and routing

The root deployment remains `/home/brandon/.cli-proxy/compose.yaml`.
Inference stays on loopback port 8317. Home management stays on loopback port 8327.
Home Center retains its original layout and left sidebar. The build adds Quotas
under Observe and API Console under Control to that native sidebar. Quotas opens
at `/management.html#/admin/quota` inside the existing Home page layout. The quota
bundle at `/quota-panel.html` contains only page content, with no sidebar or shell.
Old `/quota-dashboard.html` bookmarks redirect to the native Home quota route.
The Console at `/console.html` has a Home Center link in its existing sidebar.
There is no added top navigation bar or whole-application wrapper.

The frontend checkout is `/home/brandon/.cli-proxy/ui-source` on branch
`dev`. The backend checkout is
`/home/brandon/.cli-proxy/home-repo` on branch `dev`.
No credentials, client keys, database files, or generated panel bundles belong in Git.
Generated embedded bundles are excluded by the repository `.gitignore`.

## Building from fresh clones

The custom image uses two sibling checkouts. The Compose build file and context
allowlist below are source-controlled, contain no credentials, and do not depend
on Brandon's private deployment files. Use a new directory for this recipe:

```sh
mkdir cli-proxy-build
cd cli-proxy-build
git clone --branch dev https://github.com/bbenich/CLIProxyAPIHome.git home-repo
git clone --branch dev https://github.com/bbenich/Cli-Proxy-API-Management-Center.git ui-source
cp home-repo/tools/compose.quota-build.yaml compose.yaml
cp home-repo/tools/root.dockerignore .dockerignore
docker compose build cli-proxy-home
```

These branches must contain the customization commits; until they are pushed,
use local checkouts containing the changes at those same directory names. Run
Compose from the parent directory; `Dockerfile.quota` expects that parent as its
build context. Podman users can substitute `podman compose` in these commands.
The resulting image is `localhost/cli-proxy-home:quota-reset`. This template is
for image building only: it publishes no ports and mounts no runtime files.
Deploy the image using your own runtime Compose configuration and existing Home
database/configuration. The upstream runtime setup is documented in `README.md`;
the local deployment below continues using its private root Compose file.

## Rebuilding the existing local deployment

The root Compose build now compiles Home quota observability and the separate Console automatically.
`Dockerfile.quota` runs Bun verification/build, exports the complete original Home
panel from the pinned upstream image, and embeds the three interfaces in the Go binary.
No manually copied generated dashboard or captured static files are required.
The root `.dockerignore` is a strict allowlist for the two source checkouts;
`tools/root.dockerignore` is its source-controlled template. Credential folders
and the runtime database are excluded from the build context.

Build and start only the Home service through root Compose:

```sh
docker compose -f /home/brandon/.cli-proxy/compose.yaml build cli-proxy-home
docker compose -f /home/brandon/.cli-proxy/compose.yaml up -d --no-deps cli-proxy-home
```

The image runs focused routing and collector regression tests during its build.
The Console `bun run verify` runs its upstream tests, lint, and build.
The Home `web/quota` verification runs quota, polling, connection and API tests,
TypeScript checking, and a separate single-file production build.

## Policy

Set `routing.strategy` to `quota-reset` through the authenticated v8 config API.
This opts into the behavior described in the local extension section of
`docs/management/api.md`. Static priorities and session affinity do not govern
this strategy. Existing group permissions, model restrictions, disabled flags,
cooldowns, and concurrency rules continue to govern eligibility.

The dashboard polls snapshots every 60 seconds while visible. Home schedules
one eligible account probe per 15-second slot, across providers and manual
refresh requests. It checks the least recently attempted due account first;
never-attempted accounts come first. Successful accounts become eligible again
after a minute; with more accounts a full round takes longer. A failed attempt
rotates behind other accounts and keeps exponential backoff (5 minutes up to an
hour, plus jitter). A longer upstream Retry-After takes precedence. Partial
results with a rate-limited auxiliary endpoint receive the same backoff while
retaining usable quota windows. Manual management API collection requests queue due accounts without
bypassing freshness or failure backoff. Active leases are rechecked atomically;
an unclaimable account cannot block another eligible account in the same slot.
Provider reporting is asynchronous; the dashboard labels old observations.
Disabled accounts keep their previous observations and are not probed.
`CLI_PROXY_QUOTA_MONITOR=1` keeps idle quota collection active independently of the
routing strategy.

A fresh 100% five-hour observation without a reported reset timer takes first
priority until the provider reports the timer running or capacity below 100%.
Successful requests alone do not demote the account, because cached responses
may consume no quota. Healthy last-known weekly reset timestamps continue to
participate while stale and are labeled Estimated. A stale 100%/no-timer report
retains untouched priority as an estimate while its weekly reset remains future. Existing account/model scopes,
cooldowns, and availability still filter request candidates.

## Rollback

Before replacing the custom image, restore the previous routing configuration via
the config API; its private snapshot is `custom/routing.before-quota.json`.
Then restore the previous Home image definition from
`custom/compose.before-quota.yaml` and recreate only `cli-proxy-home`.
Do not start the legacy standalone proxy alongside Home.
The private SQLite backup `custom/home.before-quota.db` is for disaster recovery;
a normal image rollback should keep the live database so recent token refreshes
and account changes are preserved.

## Home connection reuse

Quota observability reads the pinned Home panel's `managementSession` or
`temporaryManagementSession` from browser storage without writing or copying
credentials. Only a connection to the current origin is accepted. If absent,
invalid, or for another server, the page links to Home Center's Connection page.
The pinned Home panel stores `/v0/management` as its connection base; this is
accepted as session input while quota requests use the current v8 endpoints.
The Console continues using its own existing authentication and preferences.
A storage event or window focus reloads the Home session, and stale requests
cannot replace a different session's quota cards.

## Native sidebar integration

The pinned upstream image does not ship Home frontend source. The build exports
its compiled assets and applies `tools/extend_home_panel.py` to the exact verified
entry bundle. It adds two native navigation items, changes the existing quota
route to render our content, and registers the Console switch route. The native
Home layout component is asserted unchanged. The patched entry receives a new
content-hashed filename to prevent stale browser caching. A checksum or anchor
mismatch fails the build and requires reviewing the new upstream bundle.

The quota content uses Home's saved connection; changing/disconnecting the Home
connection remounts that content. The Console's only customization is the return
link in its original sidebar; its quota, configuration, and auth pages stay intact.

## View preferences

The quota page provides compact segmented button controls for Cards/Table,
Small/Medium/Large, and account groups, styled to match Home Center. Selected
options use teal accents; all controls support keyboard focus and expose their
pressed state to assistive technology. View and size
are saved together under `home-management-center.quota-preferences` in this browser's
local storage, independently of authentication. Defaults are Cards and Medium.
Changing views retains the selected size. Reloading, leaving the page, or reconnecting
to Home preserves both choices when browser storage is available. Invalid saved
values fall back independently; unavailable storage still allows changing the current
page. Both layouts show all quota windows and account observation status; the table
scrolls horizontally on narrow screens. Density changes only card/table typography, padding, gaps, card width, progress
bars, and table cells. The heading, controls, refresh status, and routing notice
stay at a fixed size. All three content sizes use compact spacing.

## Strategy selection and priority display

Choose the strategy in Home's native System Config. All four backend-supported
strategies are selectable and survive save/reload. The build updates every shared
copy of the pinned frontend strategy parser and its config dropdown; modified
chunks receive new filenames and the entry's chunk map is updated.

The quota page reads `/quota/routing` for the selected name and global ranks/values,
using the same runtime ranking as dispatch. Group filtering is presentation-only:
Work/Personal subsets retain global ranks, including gaps. Cards and table show
rank plus reason/value; rotation uses tied priority tiers and weighted rotation
shows weights rather than a fictional fixed queue. Request-specific eligibility
and session affinity still apply. The page's strategy label replaces policy prose.

## User filter

User buttons resolve configured upstream access through `/quota/users`. Selecting
a user shows the union of their keys' credential scopes, respecting model-specific
scope intersections. User and Group selections intersect; global priority ranks
are preserved. This reads only secret-free access metadata. Users without assigned
keys show no accounts; unassigned keys never grant user access. Changes to users,
keys and scope membership appear on the next dashboard refresh. All users shows
all accounts, including those accessible only to unassigned keys. Disabled accounts
remain visible if permitted by scope, with their disabled state clearly labeled.

### Large user and group lists

User and Group use compact searchable pickers rather than expanding button lists.
Each closed picker shows its selection; opening it focuses search. Names match
case-insensitively, ignoring accents, and multiple words narrow the results.
Lists scroll within a bounded popup and render at most 100 matches. The count
indicates when more matches exist; search covers the entire loaded list, including
entries beyond the first 100. Arrow keys navigate, Enter selects, Escape closes,
and Ctrl+Home/End moves to the first/last displayed result. Tab and clicking
outside also close the popup. View and Size retain segmented buttons.
The menu uses the browser's native popover top layer, with fixed placement
bounded to the quota iframe viewport. It opens upward near the bottom and
scrolls internally, rather than clipping behind surrounding page elements.


### Recent traffic and account access

Both cards and the table show compact tokens and request counts for the last
five minutes. Hover, keyboard focus, or tap opens exact totals for the rolling
last five minutes, hour, and 24 hours, with a server snapshot timestamp. Escape,
clicking outside, or leaving the indicator closes the detail. User lists can be
scrolled inside the detail. These figures auto-refresh on the existing 60-second
page poll, independently of upstream quota resets. They reflect completed traffic
recorded by this proxy (including failed attempts), not subscription quota
percentages, other tools, or in-flight requests. Existing retention controls the
available history. A failed page read retains the previous snapshot and its
checked timestamp with a visible error; missing metrics are unavailable, not zero.

The access indicator counts distinct assigned Home users whose configured
credential/model scopes permit the account across their client keys. Hover,
focus, or tap shows their names. Multiple keys count once. Unassigned keys do not
represent a user; temporary billing/availability/cooldown state does not change
configured access. User and group filters change visible accounts, preserving
both global routing ranks and full user counts. Metrics use the page's selected
density and native popovers to avoid clipping inside the scrolling table.

### Dashboard connection and read-only refresh

The dashboard says Auto-refreshing without exposing collector or browser timing.
Its client uses GET requests only, including the Refresh button. Browser count
changes the number of cache reads, never the backend collector cadence or the
number of provider probes. Home starts one collector at process startup; browser
reads do not start collectors or request upstream collection. On a failed update
or an offline event, the live indicator disappears, a connection/update error
marks all displayed information stale, and all retained account freshness labels
become stale. A successful update restores the server's freshness labels.

Age-based dashboard warnings use a ten-minute threshold from each usable
observation's provider timestamp. The stale label and routing Estimated label
share this presentation threshold, independently of the backend's probe due
schedule and routing rank. Missing, malformed, future, or unusable observations
remain stale. A lost dashboard connection still marks all displayed accounts
stale immediately, regardless of observation age. Upstream error messages remain
visible independently of age warnings.

Usage detail abbreviates token totals with K/M/B and keeps request counts exact.
Its explanation is “Completed traffic recorded by this proxy.” Access shows
unique users plus distinct enabled credential scopes linked to each account,
with both name lists in its detail. Scope counts use all loaded bindings,
independently of dashboard filters; disabled, missing and duplicate scopes are
excluded. Model scopes are not counted. The short explanation is “Configured
access through assigned client keys and scopes.” These are cache reads only.
