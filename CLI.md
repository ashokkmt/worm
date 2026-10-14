# WORM CLI reference

Run commands from the repository root or an extracted release folder. Source-build examples use ./bin/worm (PowerShell: .\bin\worm.exe); release archives keep the binary at their root, so use ./worm (PowerShell: worm.exe) there. The binary includes the web UI and does not need Go or Node.js at runtime. Use worm -h for startup flags and worm --version to identify the build.

## Start and configure WORM

Running `worm` starts WORM in the foreground. Listener defaults bind to loopback. Marketplace access is **off by default** and does not affect ingestion. When enabled, WORM makes network requests only for explicit marketplace actions; it does not poll in the background.

| Argument | Default | Purpose |
|---|---|---|
| `-db <file>` | `data/worm.db` | SQLite database for raw events, normalized events, quarantine, and delivery state. |
| `-packs-dir <dir>` | `packs` | Active YAML parser packs and their hidden `.marketplace/` cache/management files. Paths are relative to the process working directory unless absolute. |
| `-sources-dir <dir>` | `sources` | YAML source adapter definitions. |
| `-sinks-dir <dir>` | `sinks` | YAML output adapter definitions. |
| `-workers <n>` | `4` | Processing worker count. |
| `-file <file>` | off | Read one file on startup. WORM exits after processing only when other inputs are disabled. |
| `-stdin` | `false` | Read stdin; piped input is also detected automatically. |
| `-stdin-framing line\|ndjson\|document` | `line` | Treat each line as a record or the complete bounded input as one document. |
| `-syslog-udp <address>` | `127.0.0.1:1514` | Syslog UDP listener. Use `none` to disable or an explicit interface address to accept remote senders. |
| `-syslog-tcp <address>` | `127.0.0.1:1514` | Syslog TCP listener. Use `none` to disable. |
| `-syslog-tls <address>` | `none` | Syslog TLS listener; requires `-tls-cert` and `-tls-key`. |
| `-tls-cert <file>` | unset | PEM server certificate for Syslog TLS. |
| `-tls-key <file>` | unset | Matching TLS private key. Keep it private. |
| `-tls-client-ca <file>` | unset | Client CA; enables required client-certificate verification (mTLS). |
| `-http <address>` | `127.0.0.1:8080` | HTTP ingestion API (`POST /api/v1/ingest`, `GET /api/v1/health`). `none` disables it. |
| `-http-key <token>` | unset | Require this key on HTTP ingest requests. |
| `-inbox <dir>` | `data/inbox` | Watched file-drop folder. `none` disables it. |
| `-output-file <file>` | `data/output/normalized.ndjson` | Normalized NDJSON file output. `none` disables it. |
| `-stdout[=true\|false]` | `true` | Print normalized NDJSON to the terminal. |
| `-ui <address>` | `127.0.0.1:9090` | Web UI and management API. `none` disables live management. Management HTTP needs a trusted TLS proxy before remote exposure. |
| `-admin-token <token>` | unset | Protects configured management write routes. Prefer setting `WORM_ADMIN_TOKEN` in the service environment so the secret is not in shell history. |
| `--marketplace-online` | off | Allow explicit catalog refresh and missing-artifact downloads. No internet request is made without this option. |
| `--marketplace-url <url>` | `https://ashokkmt.github.io/worm` | Marketplace registry root. Do not append `catalog.json`; WORM adds it. |
| `-d` | foreground | Start detached and write `data/worm.pid` and `data/worm.log`. |
| `-stop` / `worm stop` | — | Stop a detached WORM process. |
| `-status` / `worm status` | — | Show daemon and control API status. |
| `-replay <id\|all\|list>` | — | List quarantine records or replay one/all through the running management API. `worm replay` is the short form. |
| `-replay-all` | false | Alias for replaying all eligible quarantine records. |
| `-verify <raw-id>` | — | Recompute and compare the stored raw-payload SHA-256. |
| `-verify-event <event-id>` | — | Check normalized-event integrity and raw-event lineage. |
| `-v`, `--version`, `-h` | — | Print version or built-in flag help. |

Use the startup arguments once when starting the service. For example, the official online registry needs no custom URL:

```sh
# Local listeners, web UI, and explicit marketplace access
./bin/worm -syslog-udp 127.0.0.1:1514 -syslog-tcp 127.0.0.1:1514 \
  -http 127.0.0.1:8080 -ui 127.0.0.1:9090 \
  --marketplace-online

# Same WORM without marketplace network access
./bin/worm -syslog-udp 127.0.0.1:1514 -syslog-tcp 127.0.0.1:1514 \
  -http 127.0.0.1:8080 -ui 127.0.0.1:9090
```

To use a different static registry, add `--marketplace-url https://registry.example/root`. Marketplace write operations need an admin token configured on the running WORM. Set it before startup and in the shell used for CLI management:

```sh
export WORM_ADMIN_TOKEN="$(openssl rand -hex 32)"
./bin/worm -ui 127.0.0.1:9090 --marketplace-online
```

Save the token securely if it must survive a restart. A token supplied only to the CLI does not configure the server. The UI's token input lasts only for that browser session.

Other useful modes:

```sh
# One-shot file, with all long-running listeners disabled
./bin/worm -file testdata/sources/network-device-firewall/valid_session.log \
  -syslog-udp none -syslog-tcp none -syslog-tls none -http none -ui none -inbox none

# Detached service and later status/stop
./bin/worm -d --marketplace-online
./bin/worm status
./bin/worm stop

# Standard-input processing
cat sample.log | ./bin/worm -stdin -syslog-udp none -syslog-tcp none -http none -ui none -inbox none
```

## Decoded formats

Transport and decoded format are separate: HTTP, Syslog, files, and stdin carry bytes; WORM's decoder reads the syntax. The current decoder families are:

| Format | Behavior |
|---|---|
| Syslog | RFC 5424 and RFC 3164 envelopes; their bodies can contain another supported format. |
| JSON | Objects and bounded arrays/NDJSON records, depending on intake framing. |
| XML | Documents and bounded repeated child records selected by a pack. |
| CSV | Header/row parsing with pack-specific field mapping. |
| CEF | CEF headers and extension fields. |
| LEEF | LEEF 1.0/2.0 headers and attributes, direct or Syslog-wrapped. |
| Text | Proprietary/application text extracted through declarative pack rules. |

Decoding establishes syntax, not source meaning. A parser pack still needs to match the source and map its fields. Unknown, malformed, or ambiguous input can be quarantined instead of guessed.

## Packs, sources, and sinks

### Parser packs

Packs define how WORM recognizes and normalizes a decoded log. The ordinary file workflow is:

```sh
worm packs list
worm packs get network-device-firewall
worm packs validate -f packs/new-device.yaml
worm packs test -f packs/new-device.yaml -sample sample.log
worm packs apply -f packs/new-device.yaml
worm packs rollback
```

`apply` normally asks the running management API to validate and activate the pack. Use `--local` with `--packs-dir <dir>` only when that installation is stopped; the change will load on its next startup. Live operations use the default API at port 9090; pack commands accept `--ui <address>` when WORM listens elsewhere. Do not run local changes against a directory owned by a running process.

Marketplace commands are separate from authoring and applying arbitrary local YAML:

```sh
# Search the catalog cached under ./packs; filters can be combined
worm marketplace search firewall
worm marketplace search --vendor Cisco --product ASA --model 5500 --format syslog
worm marketplace search --installed
worm marketplace show network-device-asa

# These use the running WORM management API
worm marketplace refresh
worm marketplace fetch network-device-asa@1.0.3
worm packs fetch network-device-asa@1.0.3
worm packs install network-device-asa@1.0.3
worm packs outdated
worm packs upgrade network-device-asa
worm packs upgrade --all
worm packs remove network-device-asa
worm packs rollback
```

Search and `show` read the local signed catalog/cache under `-packs-dir`; they do not contact the server or internet. `refresh` updates that instance's cache. `fetch` downloads verified bytes without activating them. `install` validates and activates a pack on the running WORM. `remove` deactivates only marketplace-managed or bundled packs from that installation. Ordinary upgrades stay within the current major version and skip pinned packs; choose an exact version to pin it. `worm packs rollback` undoes the most recent pack transaction.

To change a stopped installation explicitly, supported pack mutation commands accept `--local`. For example:

```sh
worm marketplace import --dir ./offline-snapshot --packs-dir ./packs
worm packs fetch network-device-asa@1.0.3 --local --packs-dir ./packs
worm packs install network-device-asa@1.0.3 --local --packs-dir ./packs
```

Local `fetch` can contact the configured registry only when both `--marketplace-online` and `--marketplace-url` are provided to that command; usually it is simpler to let the running server perform downloads. Offline local install requires that the artifact has already been fetched or imported. `--local` changes files and reports that the pack is available on next startup.

Marketplace options:

| Command | Effect |
|---|---|
| `worm marketplace search [text] [filters]` | Search cached catalog. Filters: `--vendor`, `--product`, `--model`, `--category`, `--format`; `--installed` and `--json` are supported. |
| `worm marketplace show <name>` | Print cached catalog details and releases. |
| `worm marketplace refresh` | Fetch and verify the latest signed catalog through the daemon. |
| `worm marketplace fetch <name>[@version]` | Download and verify without activation. |
| `worm marketplace export --dir <dir>` | Export catalog and cached artifacts for offline transfer. |
| `worm marketplace import --dir <dir>` | Verify and import an offline snapshot into the selected packs directory. |

`--ui <address>` selects the management API for supported daemon commands; it does not open a browser and is not the marketplace URL. The default is port 9090, so omit it when WORM uses that port. CLI `search` and `show` currently read `-packs-dir` in the CLI's working context; pass `--packs-dir` if the catalog is in a non-default location.

### Sources, sinks, and replay

Sources describe where WORM reads logs; sinks describe where normalized events go. Both support `list`, `get`, `validate -f`, `test -f`, `apply -f`, and `rollback`:

```sh
worm sources list
worm sources get aws-cloudtrail-pull
worm sources validate -f sources/cloud-pull.yaml
worm sources test -f sources/cloud-pull.yaml
worm sources apply -f sources/cloud-pull.yaml
worm sources rollback

worm sinks list
worm sinks get siem-events
worm sinks validate -f sinks/http-siem.yaml
worm sinks test -f sinks/http-siem.yaml
worm sinks apply -f sinks/http-siem.yaml
worm sinks rollback
```

`apply` and `rollback` contact the running management API on port 9090. Use the server's configured admin token for protected operations. Source/sink manifests may connect to external services when tested or enabled; use trusted files and configure secrets through `secretRef` and the documented secret environment/file mechanism.

Unknown, malformed, or ambiguous logs remain in quarantine with their raw bytes. After adding and activating parser knowledge:

```sh
worm replay                  # list quarantine records
worm replay QUARANTINE_ID    # replace with an ID from `worm replay`
worm replay all              # retry all eligible records
```

Replay uses the current parser snapshot and retains the original raw-event link. Inspect its result in the UI or database. Pack activation can also request reconciliation, so review its reported result before asking for another replay.

## Log simulator and Docker

`logsim` creates sample logs without needing a real device. Build it from source and send a bounded set to a local WORM HTTP listener:

```sh
mkdir -p bin
go build -o bin/logsim ./cmd/logsim
./bin/logsim --source app --scenario mixed --format http-post \
  --target http://127.0.0.1:8080/api/v1/ingest --rate 5 --count 50
```

For the complete simulator fleet, native WORM plus Docker simulators, and the air-gap image bundle, see the current commands in [README.md](README.md#docker-and-simulation).

## Other documentation

[MARKETPLACE.md](MARKETPLACE.md) explains the marketplace purpose, owner publishing workflow, signing keys, and customer install flow. [SECURITY.md](SECURITY.md) covers deployment boundaries, credentials, and integrity limits.
