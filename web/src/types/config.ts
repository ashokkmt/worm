export interface RuntimeConfig {
  ui_address: string;
  syslog_udp: string;
  syslog_tcp: string;
  syslog_tls?: string;
  http_ingest: string;
  inbox_dir: string;
  db_path: string;
  workers: number;
  air_gapped: boolean;
  version: string;
}
