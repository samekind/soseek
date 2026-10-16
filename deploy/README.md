# Self-hosting soseek

Run one service instance with a persistent local volume. Do not share its SQLite
file over NFS or start multiple independent executors against the same file.
The CLI connects over HTTPS and needs only an individual soseek access token.
The administrator panel is served from the same origin at `/admin/`; its compiled
assets are embedded in `soseekd`. See [management setup](../docs/ADMIN.md) for
administrator authorization, sessions and credential persistence.

## Container

Build requirements: Docker with Compose. The image builds its UI with Node.js 24
and pinned pnpm, then compiles Go 1.27.1; its runtime contains no Node.js process.
It uses a non-root
UID/GID of `10001`. On a Linux Docker host, run these commands from `deploy/`:

```sh
cp config.example.json config.json
mkdir -m 700 credentials
# Put your API keys into credentials/exa.key and credentials/grok.key.
# Set a model and endpoint whose tool-call and native-search protocols you verified.
# Review per-call cost ceilings against your account's actual pricing.
chmod 600 config.json credentials/*.key
sudo chown -R 10001:10001 config.json credentials
docker compose up -d --build
docker compose exec soseek soseekd admin token-create \
  --config /etc/soseek/config.json --name owner --daily-usd 5
```

If the build host requires an HTTP proxy, supply Docker's predefined build
arguments instead of adding proxy settings to the image or the Docker daemon:

```sh
docker compose build \
  --build-arg HTTP_PROXY=http://host.docker.internal:PORT \
  --build-arg HTTPS_PROXY=http://host.docker.internal:PORT
docker compose up -d --no-build
```

Use a proxy address reachable from the builder. `host.docker.internal` is supplied
by Docker Desktop and this project's tested Colima environment; on other Linux
hosts use a reachable address or configure a host gateway for the build. These
proxy arguments are build-time inputs and are not runtime environment settings.

To use a supplied Linux AMD64 image archive instead of rebuilding on an AMD64
host, verify its accompanying checksum, load it, then start the configured service:

The historical archive recorded below predates the management panel and supports
the previously verified CLI/API service. Build the current Dockerfile for the
panel until a replacement archive has its own UI acceptance record; the version
tag alone does not prove that the new frontend is embedded.

```sh
sha256sum -c soseek_0.1.0_linux_amd64_image.tar.gz.sha256
docker load -i soseek_0.1.0_linux_amd64_image.tar.gz
# From deploy/, after preparing config.json and credentials as above:
docker compose up -d --no-build
```

Compose defaults to the local `soseek:0.1.0` image; `SOSEEK_IMAGE` can select an
explicit alternative tag. Image load does not import configuration, API keys,
personal tokens or a database. ARM64 hosts should build their native image or
explicitly provide their own emulation setup.

The example is a schema example, not a claim that every configured provider/model
combination has been tested. Declared costs are administrator ceilings, not vendor
price quotations. Search and read orders name providers; the planner names a
channel. Add/remove channels to match the APIs you actually use.

The new access token is printed once: send it directly to the user's private CLI
login flow. Do not put it in tickets, shell history, a shared log, or this repository.
Create a separate token for each user. Test `/healthz` and `/readyz` on
`http://127.0.0.1:8080`, then make a real authenticated search before treating the
deployment as accepted.

Place an HTTPS reverse proxy in front of the loopback port. For Caddy:

```caddyfile
search.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

Keep request buffering and short proxy timeouts disabled for SSE. The compose
file publishes only loopback, caps container logs at roughly 24 MB, and stores
database data in its named `data` volume. Container deletion does not delete that
volume; `docker compose down -v` does, so do not use it for routine upgrades.

For the admin panel behind a container proxy, configure `trusted_proxies` with
the actual proxy source's precise `/32` (IPv4) or `/128` (IPv6) CIDR. A service
inside Docker may see the bridge gateway or the reverse-proxy container address
even when the published host port is loopback. Do not assume the gateway address
or trust an entire subnet. The default trusts only loopback forwarded HTTPS
headers; an untrusted source cannot establish a remote management session by
claiming `X-Forwarded-Proto: https`. See [proxy setup](../docs/ADMIN.md#https-反向代理).

## Build the embedded panel

When building native release binaries from source, use Node.js 24 or newer and
the pnpm version pinned in `web/package.json`:

```sh
# Run from the repository root.
./scripts/build-web.sh
go build -o bin/soseekd ./cmd/soseekd
```

The script performs a frozen dependency install and TypeScript/Vite build, checks
the `/admin/` entry assets, and copies `web/dist` into `internal/adminui/dist`.
Generated assets are ignored by Git; `placeholder.txt` remains tracked so ordinary
Go checks can run without a frontend toolchain. A binary built with only that
placeholder returns HTTP 503 at `/admin/` with build instructions. The release
script and CI always build the real UI first; Docker builds and embeds it in its
separate frontend stage.

## Authorize the first administrator

Existing caller tokens do not become administrators automatically. After creating
or choosing a principal, run the local bootstrap command on the server:

```sh
docker compose exec soseek soseekd admin token-list --config /etc/soseek/config.json
docker compose exec soseek soseekd admin grant \
  --config /etc/soseek/config.json --id PRINCIPAL_ID
```

For native deployments use the same commands without `docker compose exec soseek`,
as the service user. Then open `/admin/` over HTTPS and enter that principal's
existing token. The browser receives a 12-hour HttpOnly, SameSite=Strict session
cookie, marked Secure over HTTPS; it does not persist the access token in browser
storage or URLs. Administrators can inspect every user's tasks and results and
manage personal tokens, quotas and existing upstream credential references.
Ordinary callers retain the original per-principal API isolation. Browser users
cannot grant administrator roles or add new provider/channel definitions.

## Native systemd

Install the release binary at `/usr/local/bin/soseekd`; create a dedicated
`soseek` system user. Place configuration and secrets under `/etc/soseek`,
readable only by that user, and install `soseek.service`. For native deployments,
change `listen` to a loopback address such as `127.0.0.1:8080`.

```sh
sudo useradd --system --home-dir /var/lib/soseek --shell /usr/sbin/nologin soseek
sudo install -d -o soseek -g soseek -m 700 /etc/soseek/credentials /var/lib/soseek
sudo install -o soseek -g soseek -m 600 config.json /etc/soseek/config.json
sudo install -m 644 soseek.service /etc/systemd/system/soseek.service
sudo systemctl daemon-reload
sudo systemctl enable --now soseek
```

Copy keys privately with mode `0600`, owned by `soseek`. The sample service
limits log rate; set journal storage limits according to the host's policy.
Server configuration contains no access tokens: token hashes live in SQLite.

## Backup, restore, upgrade

Use `soseekd backup --config /etc/soseek/config.json --out /path/snapshot.db`.
It produces a consistent SQLite snapshot and verifies its integrity. Never copy
only a live `soseek.db` file while omitting its WAL. Backups contain private
queries, complete results, retrieved page bodies and token hashes; keep them at
mode `0600` on protected storage. SQLite is the authoritative result store.
Artifact JSON files are derived exports: the authenticated download endpoint can
return their content from SQLite even when the export files are absent. This
command does not copy configuration or upstream secrets. Preserve recoverable
configuration and retain the needed secrets independently in your secret store.

Panel credential changes add private override files under
`dirname(database)/credentials`, normally `/var/lib/soseek/credentials`, with
directory mode `0700` and file mode `0600`. This location is in the persistent
data volume and is separate from the original read-only `/etc/soseek/credentials`
mount. Preserve it during deployment, binary rollback and restoration; replacing
the image must not reset overridden keys or disabled-credential markers. Include
it in a separate protected secret backup: **SQLite snapshots do not contain this
directory**, and database-only recovery does not restore panel key changes.

For the container, use a private temporary directory on the data volume, then
copy the completed snapshot out:

```sh
docker compose exec soseek mkdir -p -m 700 /var/lib/soseek/backups
docker compose exec soseek soseekd backup --config /etc/soseek/config.json \
  --out /var/lib/soseek/backups/snapshot.db
docker compose cp soseek:/var/lib/soseek/backups/snapshot.db ./snapshot.db
chmod 600 snapshot.db
```

For restoration, stop the only service instance. Preserve the current database,
WAL, SHM and artifact directory as a rollback set. Install the verified snapshot
as `soseek.db` with the service UID/GID and mode `0600`; remove stale WAL/SHM only
after preserving that rollback set. Artifact files are optional derived copies;
start the service and test authentication, ownership isolation and a real query.
Restore the matching private credential override directory as well as the original
configuration/secret references. The reference paths or environment names must
remain consistent with the deployment being restored. Database snapshots also
retain administrator roles and session hashes, so review access changes made
after the snapshot before reopening management access.
Recovery may mark interrupted upstream attempts uncertain instead of replaying
possibly billed calls. Restoring an older database also restores older access
token state: repeat any revocations or quota changes made after the snapshot.

Before upgrading, take a verified snapshot and retain the prior binary/config.
Do not assume an old binary can read a database after a destructive migration.
The recommended initial retention is seven days. Same-host snapshots help with
operator errors; also copy them to separate protected storage to cover host loss.

## Container acceptance record

These records predate the embedded management panel. They establish the recorded
CLI/API image behavior and do not establish the newer `/admin/` flows, frontend
bundle or credential-overlay recovery; those require a separate updated build
and acceptance run.

On 2026-10-17 at 00:43 UTC+08:00 the source snapshot below was built and exercised
on Linux ARM64 via Colima. An isolated Compose project used the service's non-root user,
read-only root filesystem, dropped capabilities, resource limits and persistent
data volume. Its provider was a deterministic local Exa HTTP fixture; no real
provider calls or paid requests were made during this container check.

The check passed readiness/health, missing/invalid-token rejection, two-user data
isolation, a complete fixture result with page body, idempotent submission,
restart persistence, online SQLite backup and integrity checking, and restoration
from that snapshot. After restoration the same token could retrieve the full
result and download its artifact without an export file on disk. Runtime image
environment and service logs did not contain the build proxy or test credentials.
The disposable containers, network and test volumes were then removed. This
validates container packaging and persistence; actual upstream provider behavior
and public HTTPS still require deployment-specific acceptance.

Recorded image ID:
`sha256:1fe10631a47547994fffa2dd06dc3022b164162f174f1d41b2e140792be735e6`.
Recorded source-tree SHA-256 (`go.mod`, `go.sum`, `cmd`, `internal`, `schemas`):
`66cec429b0564e0236d5ee791eab858178f2d277558201b1a398ef39ab59f8c1`.
The ARM64 record covers that earlier source snapshot, before the later inspect
matching, persisted planner trace and routing refinements.

The final Linux AMD64 `soseek:0.1.0` image was separately rebuilt and checked on
2026-10-17 at 01:16 UTC+08:00. The build was capped at 1 GiB RAM and 1.5 CPU cores.
Five focused checks passed: AMD64/non-root image configuration without a runtime
proxy, health/readiness and authentication, one local Exa fixture request with its
complete page body, token/result persistence across restart, and logs without
credentials. It used a dedicated network and a loopback-only temporary port;
all test containers, networks and volumes were removed afterward. No live
provider requests were made in this image check.

Final AMD64 image ID:
`sha256:b1b4dd2028934ce4ebbab3501e109f327232e4233b9f1cc7cfd75c77312ceafb`.
Final source-tree SHA-256:
`c824fc12b2cdb932a0e14ed8a814a23b5edd65f4c67ce157216c4594d46e76cc`.
The supplied `soseek_0.1.0_linux_amd64_image.tar.gz` SHA-256 is
`1886ed710fbd7107bb6ef7345089bf0d5f50a19ac79284246b164fe4bcdcdc2e`.
The archive's tag, architecture and non-root configuration were inspected; its
layers contain no server configuration, test credentials or database. The earlier
ARM64 full persistence/backup/restore checks and this final AMD64 image check are
separate evidence records, not a claim that both architectures share one image ID.
