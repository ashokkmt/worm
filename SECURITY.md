# Security policy

## Reporting a vulnerability

Please do not publish exploit details, raw logs, credentials, or personal data in a public issue. Use GitHub's **Report a vulnerability** option under this repository's **Security** tab if it is enabled. If private reporting is unavailable, open a minimal issue requesting a private contact channel **without** technical details or secrets. Include the affected release/commit, impact, and reproduction steps only in the private report.

Maintainers have not published a response-time or supported-version guarantee. For a fix, use the newest available release and verify its asset checksum; do not assume an older release receives backports.

## Deployment warning

WORM is currently a prototype. Its default management (`:9090`) and HTTP ingestion (`:8080`) listeners bind to all interfaces; administrative and ingestion tokens are optional, and the management API uses plain HTTP. The current implementation has known gaps affecting management-token exposure, raw payload access through traces, unauthenticated connection testing, and whether buffered sink delivery has actually reached external/disk storage. HTTP acceptance currently uses SQLite WAL `synchronous=NORMAL`, which is not a power-loss durability guarantee. Treat hash verification as a local consistency check, not proof against someone who can replace both data and stored hashes.

Until these issues are fixed and retested, keep the UI/control listener on a trusted, loopback-only interface or private network; do not publish ports 8080/9090 from the demo Compose file to untrusted hosts. For a local-only native run, explicitly use `-http 127.0.0.1:8080 -ui 127.0.0.1:9090` and disable unused syslog listeners with `-syslog-udp none -syslog-tcp none -syslog-tls none`. Restrict database and file permissions, use a trusted reverse proxy if remote access is necessary, and avoid putting credentials directly in shell history or YAML. Source/sink manifests should use `secretRef` and private environment/file secrets. No supplied flag alone should be treated as a complete production security control in the current release.

Do not rely on this release as the only archive for regulated, forensic, or loss-intolerant logs. Keep an independent source archive and validate delivery and recovery in your own environment.
