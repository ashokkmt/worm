# WORM CLI reference

Run commands from the repository root or an extracted Release directory so the default `packs/`, `sources/`, and `sinks/` paths resolve. The examples below use `worm` as a placeholder: run `./worm` on Linux/macOS, `.\worm.exe` in Windows PowerShell, or an installed `worm` on your `PATH`. `worm -h` prints the Go flag parser's current list. Arguments use a single hyphen; `-v` and `--version` are the dedicated version aliases.

## Startup flags

`worm` without a management subcommand starts the processing engine. Listener addresses such as `:8080` bind all interfaces; prefer `127.0.0.1:8080` for a local-only test. `none` disables the indicated optional listener/output, but see each row for its exact behavior.

| Flag | Default | What it does |
|---|---|---|
| `-db <path>` | `data/worm.db` | SQLite database for raw payloads, hashes, normalized events, quarantine records, and delivery state. The parent directory is created. Use this same path for later verification. |
| `-packs-dir <dir>` | `packs` | Load YAML parser packs at startup; packs decide whether and how decoded fields map to the common event. `packs` subcommands also use this directory. |
| `-sources-dir <dir>` | `sources` | Load declarative `Source` YAML for cloud pull, Kafka input, or Syslog TLS. An example file with `enabled: false` does not start an adapter. |
| `-sinks-dir <dir>` | `sinks` | Load declarative `Sink` YAML for HTTP, Kafka, Parquet, or NDJSON output. Disabled examples do not deliver events. |
| `-workers <n>` | `4` | Number of concurrent processing workers. It does not add listener ports or turn one SQLite database into a distributed store. |
| `-file <path>` | off | Ingest one file at startup. If other listeners and enabled sources are disabled, WORM drains the pipeline, prints final accounting, and exits; otherwise the server continues running. |
| `-stdin` | `false` | Read records from standard input. A pipe is detected automatically; use the flag to request stdin explicitly. |
| `-stdin-framing line\|ndjson\|document` | `line` | `line` and `ndjson` currently submit each nonempty line as one record; `document` submits the entire bounded stream as one payload, useful for multiline JSON/XML/CSV. Detection and decoding happen afterward. |
| `-syslog-udp <addr>` | `:514` | Plaintext Syslog UDP listener (best effort). Use `:1514` for a non-privileged local demo or `none` to disable. |
| `-syslog-tcp <addr>` | `:514` | Plaintext Syslog TCP listener with newline and octet-counted framing. Use `:1514` for a non-privileged demo or `none` to disable. |
| `-syslog-tls <addr>` | `none` | Opt-in encrypted Syslog TLS listener. Requires `-tls-cert` and `-tls-key`; use a suitable unprivileged port such as `:7514`. |
| `-tls-cert <path>` | unset | PEM X.509 server certificate for `-syslog-tls`. |
| `-tls-key <path>` | unset | Matching private key for `-syslog-tls`. Keep it out of the repository. |
| `-tls-client-ca <path>` | unset | Optional PEM client CA. If supplied, the TLS listener requires and verifies client certificates (mTLS). |
| `-http <addr>` | `:8080` | HTTP intake listener: `POST /api/v1/ingest` and `GET /api/v1/health`. Use `none` to disable. This is separate from the management UI/API. |
| `-http-key <token>` | unset | Require `X-WORM-Key: <token>` or `Authorization: Bearer <token>` on HTTP ingest POSTs. Keep tokens out of checked-in files and shell history. |
| `-inbox <dir>` | `data/inbox` | Watch a spool directory for dropped files; WORM uses `processing/`, `processed/`, and `failed/` subdirectories. Use `none` to disable. |
| `-output-file <path>` | `data/output/normalized.ndjson` | Write normalized events as line-delimited JSON. Use `none` to disable this file sink. |
| `-stdout[=true\|false]` | `true` | Stream normalized JSON to stdout. `-stdout=false` suppresses it; the database and other configured outputs remain active. |
| `-ui <addr>` | `:9090` | Management web UI and control-plane API listener, for example `-ui 127.0.0.1:9090` then open `http://127.0.0.1:9090`. `none` disables the UI/API; live apply and replay commands then cannot contact it. |
| `-admin-token <token>` | unset | Optional token for protected control-plane mutation routes. **Do not treat this as a complete security boundary in the current release**; see [SECURITY.md](SECURITY.md). |
| `-d` | `false` | Detach into a child process, write PID to `data/worm.pid`, append logs to `data/worm.log`, and return the terminal. The child gets the other startup flags. |
| `-stop` | `false` | Stop the detached process identified by `data/worm.pid`. `worm stop` and `worm --stop` are aliases. |
| `-status` | `false` | Show detached-process status and a management health probe. `worm status` and `worm --status` are aliases; the verb form probes the default `:9090`. |
| `-replay <id\|all\|list>` | unset | List or replay quarantine entries through the running management API. Bare `worm replay` / `worm -replay` lists; `worm -replay=list` also lists. `worm replay <id>` reprocesses one and `worm replay all` reprocesses eligible entries in bulk. |
| `-replay-all` | `false` | Bulk-replay alias for `worm replay all`. |
| `-verify <raw-id>` | unset | Open `-db`, recompute SHA-256 over stored raw bytes, and compare with the stored receipt hash. This action exits without starting listeners. |
| `-verify-event <event-id>` | unset | Open `-db` and verify the normalized event plus its raw parent/lineage; exits without starting listeners. |
| `-v`, `--version` | — | Print `WORM CLI Version: <version>` and exit. Use as the sole argument. |
| `-h` | — | Print the Go flag help text and exit. |

Examples:

```sh
# Local-only HTTP intake and UI; no Syslog or watched inbox.
worm -syslog-udp none -syslog-tcp none -syslog-tls none -inbox none -http 127.0.0.1:8080 -ui 127.0.0.1:9090

# One-shot file; use no other listener/source if you want it to exit afterward.
worm -file testdata/sources/network-device-firewall/valid_session.log -syslog-udp none -syslog-tcp none -syslog-tls none -http none -ui none -inbox none

# Same listener settings, but return the terminal immediately.
worm -d -syslog-udp none -syslog-tcp none -syslog-tls none -inbox none -http 127.0.0.1:8080 -ui 127.0.0.1:9090
worm status
worm stop
```

The `Source` YAML directory can also start enabled intake adapters, so disabling the direct listener flags alone is not always enough to make `-file` a one-shot run. Use an empty `-sources-dir` for an isolated batch test.

## Decoded formats

Transport and format are different: HTTP, Syslog, files, and stdin *carry* bytes; the decoder determines their syntax. The current registry has seven decoder families:

| Format | Current decoding behavior |
|---|---|
| Syslog | RFC 5424 and RFC 3164 envelopes; a body can contain another supported format, such as CEF or LEEF. |
| JSON | Object and bounded multi-record forms such as arrays/NDJSON, depending on intake framing. |
| XML | Single documents and bounded repeated child records selected by a pack. |
| CSV | Header/row parsing, with pack-specific field mapping. |
| CEF | CEF header and extension fields. |
| LEEF | LEEF 1.0/2.0 headers and attributes, direct or Syslog-wrapped. |
| Text | Proprietary or application-specific text extracted through declarative pack rules. |

Detection/decoding establishes syntax, not source meaning. A matching parser pack is still needed for trustworthy normalized output. An unknown, ambiguous, malformed, or unmapped event can be quarantined rather than guessed into a source category. The original accepted bytes remain linked to the resulting normalized event or quarantine record.

## Packs, sources, and sinks

- A **pack** (`packs/*.yaml`) describes *what an event means*: match rules, field extraction/types, OCSF mapping, and preservation of unmapped fields. This is how a new supported-format device or app can be onboarded without rebuilding the CLI.
- A **source** (`sources/*.yaml`) describes *where raw events come from*: currently `cloud_pull`, `kafka_input`, and `syslog_tls`. Basic HTTP, Syslog UDP/TCP, file, and stdin inputs are also available directly through startup flags.
- A **sink** (`sinks/*.yaml`) describes *where normalized events go*: currently `http_output`, `kafka_output`, `parquet_output`, and `ndjson_output`. `-stdout` and `-output-file` provide simple local outputs without sink YAML.

Each family supports `list`, `get <name>`, `validate -f <file.yaml>`, `test -f <file.yaml>`, `apply -f <file.yaml>`, and `rollback`. The first word may also be written `-packs`/`--packs`, `-sources`/`--sources`, or `-sinks`/`--sinks`; the bare words shown below are easier to read:

| Operation | Packs | Sources and sinks |
|---|---|---|
| `list` / `get` | Inspect pack definitions (running API if reachable, otherwise local pack files). | Inspect manifests in the respective local directory. |
| `validate -f` | Check a proposed parser pack before installation. | Parse and validate a proposed resource locally. |
| `test -f` | Test a pack against `-sample <log-file-or-text>`. | Probe the configured target/path; this can make network or filesystem access. Use only trusted manifests. |
| `apply -f` | Install a pack into `-packs-dir`, then attempt activation on the local `:9090` control plane; if the daemon is unavailable, it loads on the next startup. | Requires a running `:9090` control plane; validates, applies, and reconciles the resource live. |
| `rollback` | Restore the previous active pack snapshot through the running control plane. | Restore the previous Source/Sink configuration snapshot through the running control plane. |

```sh
worm packs list
worm packs get network-device-firewall
worm packs validate -f packs/network-device-firewall.yaml
worm packs test -f packs/app-payment-gateway.yaml -sample testdata/sources/app-custom-json/valid_after.json
worm sources list
worm sources validate -f sources/cloud-pull.yaml
worm sinks list
worm sinks validate -f sinks/http-siem.yaml
```

The included source/sink examples are disabled until explicitly configured and enabled. `sources apply` and `sinks apply` use `WORM_ADMIN_TOKEN` when one is set for the control API. Pack apply and replay CLI requests do **not** currently send that token, so they may fail against a token-protected control plane; this is a known release limitation, not a reason to leave an exposed management port unauthenticated. See [SECURITY.md](SECURITY.md).

## Replay: recover an event after adding parser knowledge

Replay is a core WORM workflow. When a recognized transport delivers an event but detection, pack matching, conversion, or schema validation fails, WORM retains its raw bytes and records a quarantine entry instead of silently discarding it. You can then add or correct a YAML pack and reprocess the stored bytes; no source-side resend is required.

1. Start WORM with a management API (`-ui 127.0.0.1:9090`) and keep the database and pack directory from the original ingestion. Use `worm replay` or the Quarantine UI to find a quarantine ID and reason.
2. Create a pack for the event's supported format, then run `worm packs validate -f <new-pack.yaml>` and `worm packs test -f <new-pack.yaml> -sample <saved-sample>`. The [existing packs](packs/) are examples of the YAML shape.
3. Run `worm packs apply -f <new-pack.yaml>` to install and activate the pack. In the current CLI, successful activation also requests a bulk quarantine reconciliation; inspect its reported replay counts. If no daemon is running, restart WORM to load the pack first.
4. If the event remains quarantined, run `worm replay <quarantine-id>` or `worm replay all`. Replay verifies the stored raw hash, decodes the appropriate child record, matches the *current* pack snapshot, normalizes and validates, persists the result, and removes a resolved quarantine entry.
5. Inspect the event/trace UI or query the management API, then use `worm -db data/worm.db -verify <raw-id>` and `worm -db data/worm.db -verify-event <event-id>` for local consistency checks.

```sh
worm replay
worm packs validate -f new-device.yaml
worm packs test -f new-device.yaml -sample sample.log
worm packs apply -f new-device.yaml
worm replay <quarantine-id>
```

The placeholders above are intentional: an ID exists only after an actual quarantine, and the new pack must match that event. Bulk replay is for eligible records and can leave failed ones in quarantine. Replay preserves the original raw-event linkage; it cannot repair events lost before WORM accepted them. See [SECURITY.md](SECURITY.md) for current hash, authentication, and durability limits.
