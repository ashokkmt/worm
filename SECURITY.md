# Security policy

## Reporting a vulnerability

Please do not publish exploit details, raw logs, credentials, or personal data in a public issue. Use GitHub's **Report a vulnerability** option under this repository's **Security** tab if it is enabled. If private reporting is unavailable, open a minimal issue requesting a private contact channel **without** technical details or secrets. Include the affected release/commit, impact, and reproduction steps only in the private report.

Maintainers have not published a response-time or supported-version guarantee. For a fix, use the newest available release and verify its asset checksum; do not assume an older release receives backports.

## Deployment warning

WORM is currently a prototype. Native management, HTTP ingestion, and plaintext Syslog listeners default to loopback addresses. Explicitly binding them to a public/interface address changes that boundary: HTTP management is not TLS, HTTP ingestion accepts unauthenticated requests unless `-http-key` is set, and regular event listings remain readable. Use a trusted TLS-terminating proxy and network policy before remote exposure, require tokens where supported, and do not publish demo Compose ports to untrusted hosts.

The management config response no longer returns the configured admin token; raw-event traces and connection probes require admin authorization; management JSON bodies are capped at 1 MiB. Sink outbox delivery now waits for each destination's flush, with at-least-once semantics (a destination can receive a duplicate if it accepted a write but the local acknowledgement could not be committed). SQLite WAL uses `synchronous=FULL` for the raw-store commit boundary. File-spool processing resumes files left in `processing/` and deduplicates already-committed offsets. These measures have regression tests, but do not replace live broker, remote SIEM, storage-failure, power-loss, or hosted-release validation.

For a local-only native run, keep the defaults or explicitly use `-http 127.0.0.1:8080 -ui 127.0.0.1:9090` and disable unused Syslog listeners with `-syslog-udp none -syslog-tcp none -syslog-tls none`. Restrict database and file permissions; avoid putting credentials in shell history or checked-in YAML. Source/sink manifests should use `secretRef` and private environment/file secrets. Treat hash verification as a local consistency check, not proof against someone who can replace both data and stored hashes.

Do not rely on this release as the only archive for regulated, forensic, or loss-intolerant logs. Keep an independent source archive and validate delivery and recovery in your own environment.
