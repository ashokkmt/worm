# WORM Parser Marketplace

WORM's parser marketplace is a public catalog of YAML parser packs maintained by the WORM owner. It gives users one place to search across many vendors, products, log formats, and documented models, then install only the packs they need. This lets the catalog grow independently of WORM binary releases when a pack uses an existing decoder. A format that needs a new decoder still needs a WORM code change and release.

The catalog currently contains **124 unique parser packs across 116 researched source profiles**, with **131 published pack releases**. A source profile may reuse an existing parser identity when several vendor/model scopes share a format and matching rules. The marketplace does not install anything by itself: each WORM operator decides which packs to activate.

## Marketplace address and files

The public registry is hosted at [https://ashokkmt.github.io/worm](https://ashokkmt.github.io/worm). The same Pages site displays the project README at its homepage and serves the signed `catalog.json` and immutable versioned YAML files such as:

```text
https://ashokkmt.github.io/worm/packs/network-device-asa/1.0.3/pack.yaml
```

Browse the human-readable pack index at [https://ashokkmt.github.io/worm/packs/](https://ashokkmt.github.io/worm/packs/). Select a pack to see its published versions and links to each raw YAML artifact. The versioned artifact URL is intended to display YAML directly; GitHub Pages does not generate folder listings automatically.

The repository keeps the same searchable coverage and release list in [marketplace/INDEX.md](marketplace/INDEX.md). It lists each pack's description, published versions and changelogs, vendor/product, category, format, models, and tags. The publisher regenerates this file from the signed catalog with `go run ./cmd/marketplace -write-index`, and every signed publish refreshes it as well.

GitHub Pages serves static files, so the registry needs no running WORM server. The root `packs/` directory in the source repository holds the owner's current pack YAML; it is not copied into binary archives or the Docker image. Those customer packages start with an empty `packs/` directory, where marketplace-installed and user-created YAML files are stored. `marketplace/metadata.json` holds searchable coverage and release notes. `marketplace/releases/` preserves old published YAML versions. The publisher generates `marketplace-site/`; GitHub Actions publishes that generated folder. The marketplace's generated site is not a customer's active pack folder.

## Start WORM with or without online marketplace access

Online access is disabled by default. Start the normal WORM service with these options to allow explicit registry refreshes and downloads:

From a source checkout built using the README steps, the executable is `./bin/worm`. In the examples below, replace `./worm` with `./bin/worm` when starting from the checkout; downloaded release archives use `./worm` from the extracted folder.

```sh
./worm \
  -syslog-udp 127.0.0.1:1514 -syslog-tcp 127.0.0.1:1514 \
  -http 127.0.0.1:8080 -ui 127.0.0.1:9090 \
  --marketplace-online \
  --marketplace-url https://ashokkmt.github.io/worm
```

The URL is the registry root; WORM adds `catalog.json` and the artifact paths itself. The default URL is already the address above, so you can omit `--marketplace-url` when using the official registry. Put the options in the service configuration or the WORM startup command once. Normal marketplace commands do not need the URL repeated.

For a local-only or air-gapped run, omit `--marketplace-online`:

```sh
./worm -syslog-udp 127.0.0.1:1514 -syslog-tcp 127.0.0.1:1514 \
  -http 127.0.0.1:8080 -ui 127.0.0.1:9090
```

WORM can search the signed catalog bundled in its binary and verified catalog/artifacts already cached under its packs directory. Refresh and uncached downloads need online access. No marketplace request happens during ordinary startup, ingestion, or cached search.

Marketplace writes through the management API require an admin token configured for that WORM instance. A customer creates and protects their own token; it is unrelated to the owner's publishing key. For example, configure `WORM_ADMIN_TOKEN` in the service environment before starting WORM. Keep it private. CLI requests read `WORM_ADMIN_TOKEN`; the web UI accepts the same token for the current browser session. Do not put it in a shared command or in the marketplace.

## How users browse and manage packs

Run commands from the extracted WORM release directory so its default `packs/` directory is selected. The examples use the default management address `127.0.0.1:9090`; if the WORM UI/API listens elsewhere, add `--ui <address>` to supported pack/marketplace CLI commands.

```sh
# Search the currently cached catalog (works offline)
./worm marketplace search firewall
./worm marketplace search --vendor "Cisco" --format syslog

# See coverage and available releases
./worm marketplace show network-device-asa

# Update the catalog, then download without activating
./worm marketplace refresh
./worm packs fetch network-device-asa@1.0.3

# Install and activate a release on this WORM instance
./worm packs install network-device-asa@1.0.3

# Review and manage packs
./worm packs list
./worm packs outdated
./worm packs upgrade network-device-asa
./worm packs remove network-device-asa
./worm packs rollback
```

`search` and `show` read the catalog under the local `-packs-dir` (the default is `packs`). `refresh`, `fetch`, `install`, `upgrade`, `remove`, and rollback contact the WORM management API by default. The running WORM owns the active directory and applies an install to its parser snapshot, so new events can use the pack without restarting. `fetch` only caches a verified YAML file. `remove` deactivates a marketplace-managed pack from that one WORM installation; it does not delete the publisher's source YAML, the hosted artifact, or copies installed on other computers. Logs that need a removed parser may be quarantined.

To manage a stopped installation deliberately, pass `--local` to supported mutation commands and `--packs-dir` with that installation's directory. Do not use local mode while another WORM process is running against the same pack directory. Local changes load the next time WORM starts. A daemon/API error does not switch the operation to a different local directory.

The UI offers the same catalog discovery and pack actions through the WORM API. It does not contact GitHub Pages directly; the WORM server performs registry requests. Keep the UI on a trusted network or behind a TLS proxy when remote access is needed.

## How the owner adds and releases packs

Only the software owner publishes to the official marketplace. Customers can install packs but cannot upload or replace official releases. The owner-authored inputs are:

| Path | Purpose |
|---|---|
| `packs/<name>.yaml` | Owner's current parser YAML; the publisher releases it, but customer binaries do not bundle it. |
| `marketplace/metadata.json` | Vendor, product, model, tags and release changelog/compatibility metadata. |
| `marketplace/releases/<name>/<version>.yaml` | Exact YAML bytes for earlier published releases. Keep these files. |
| `internal/marketplace/catalog.json` | Signed seed catalog checked into the repository and embedded in WORM builds. |

For each new pack, write and test its YAML using the existing parser schema and representative logs. Add only confirmed vendor/model coverage to `metadata.json`, under the pack name and the release key `name@version`. For a new version, archive the previous YAML byte-for-byte under `marketplace/releases/<name>/<old-version>.yaml`, then update the current file's version and metadata. A published version is immutable: changed YAML needs a new version.

Model values are searchable profile scopes, not certification that every device or firmware version has been tested. The initial market-data profiles use synthetic fixtures; verify real vendor captures before describing a model as production-certified.

For one local signed release, the owner runs the publisher from the repository root after configuring the owner signing key file:

```sh
go run ./cmd/marketplace \
  -packs packs \
  -metadata marketplace/metadata.json \
  -out internal/marketplace/catalog.json \
  -site marketplace-site \
  -key-file "$HOME/.config/worm/marketplace-signing-key.b64"
```

This command validates the packs and release history, calculates each artifact's SHA-256, signs the catalog, updates the signed seed, and writes the catalog index, version pages, and static files under `marketplace-site/`. Serving this directory locally is useful for testing, but does not publish it to other users.

For changed existing packs, prepare the release **before editing the YAML**. List every pack filename that you plan to change; the command archives the exact current bytes, bumps each version, and creates release metadata in one batch:

```sh
go run ./cmd/marketplace \
  -prepare-release \
  -changelog "Tighten parser matching and normalization" \
  market-025-aws-vpc-flow.yaml market-057-linux-audit-auth.yaml
```

Then edit those YAML files without changing the assigned version. After validation, sign the finished content once with the regular publisher command:

```sh
go run ./cmd/marketplace -key-file "$HOME/.config/worm/marketplace-signing-key.b64"
```

When a current file still matches its latest signed version, preparation archives those exact bytes directly. If it was already edited, the command tries `marketplace/releases/`, the generated local `marketplace-site/`, then the official registry, and accepts prior bytes only when their digest matches the signed catalog. It stops if it cannot recover them. Older history is also digest-checked. The command refuses changed files that were not selected, and it can be safely rerun after preparation without overwriting edits. New pack names still use the regular publishing workflow. The signing key is only needed for the final signing step and must remain private.

For a public release, commit the pack YAML, metadata, any previous-version archive, related fixtures/tests, and updated signed seed. Push the commit to `main`; `.github/workflows/marketplace.yml` runs checks and creates a fresh site from the committed files. It uses the `WORM_MARKETPLACE_SIGNING_KEY` secret from the protected `marketplace-publish` environment and deploys the resulting static files to GitHub Pages. A manual **Actions → Publish parser marketplace → Run workflow** run also uses the selected committed branch; it cannot see uncommitted files on a laptop, and the selected branch must be allowed by the `github-pages` environment deployment rules. The generated `marketplace-site/` directory is not committed.

To retire a pack, mark each of its published releases `"withdrawn": true` in `marketplace/metadata.json`, preserve the old YAML artifacts and catalog history, and remove its current YAML from root `packs/` when it should stop appearing in future marketplace catalogs. Update affected tests and the signed seed, then publish the catalog update through the same workflow. After users refresh, they cannot newly install withdrawn releases. The update never reaches into their computers to uninstall an active pack; each customer decides when to remove it.

The commands above describe the public owner workflow; the local test and published workflow use the same publisher inputs and signing checks.

## How signing protects users

The owner keeps an Ed25519 private key outside the repository and customer machines. Local publishing reads that key file; GitHub Actions reads the matching base64 private key from the protected environment secret. The matching public key is built into WORM as a trust root. The workflow should be limited to owner-approved publishing, and untrusted pull requests must never receive the secret.

WORM verifies the catalog signature before trusting its pack names, versions, and download paths. Each YAML artifact also has a SHA-256 digest in the signed catalog; WORM checks the exact downloaded bytes against that digest, then validates the YAML before activating it. The signature establishes that the catalog came from a trusted publisher; the digest detects changed or damaged artifact bytes. Downloads use registry-relative paths and do not execute code.

Customers' admin tokens authorize changes to their own WORM API. Those tokens are never sent to the public registry. They do not grant publishing rights and cannot sign a catalog. The signing key is for the owner only.

Signing protects the origin and integrity of marketplace metadata and artifacts. It does not prove a pack's parsing rules are correct or safe for every log stream; review coverage and test packs with representative data before activation. See [SECURITY.md](SECURITY.md) for deployment and credential guidance.
