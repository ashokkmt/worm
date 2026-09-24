import React, { useEffect, useState } from 'react';
import { Settings, ShieldCheck, CheckCircle2, Network, Play, RotateCcw, AlertTriangle, ArrowRight, Check } from 'lucide-react';
import { RuntimeConfig } from '../types/config';
import { ConnectionSummary } from '../types/connection';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';

const SAMPLE_HTTP_YAML = `apiVersion: worm.io/v1
kind: Connection
metadata:
  name: siem-events
  version: 1.0.0
  description: HTTP SIEM delivery sink for enterprise event ingest
spec:
  type: http_output
  enabled: true
  delivery:
    format: ndjson
    batchEvents: 500
    flushInterval: 1s
    retry:
      maxAttempts: 3
      minBackoff: 250ms
      maxBackoff: 30s
  endpoint:
    url: http://127.0.0.1:8080/events
    timeout: 10s
    secretRef: siem-http-token
`;

const SAMPLE_KAFKA_YAML = `apiVersion: worm.io/v1
kind: Connection
metadata:
  name: normalized-kafka
  version: 1.0.0
  description: Kafka output topic for normalized OCSF events
spec:
  type: kafka_output
  enabled: true
  brokers:
    - kafka:9092
  topic: worm.normalized.v1
  key: worm.source_id
  acks: all
  idempotent: true
  compression: zstd
  batchBytes: 1048576
  linger: 5ms
  secretRef: kafka-client-auth
`;

const SAMPLE_PARQUET_YAML = `apiVersion: worm.io/v1
kind: Connection
metadata:
  name: local-parquet-lake
  version: 1.0.0
  description: Parquet columnar data lake output partitioned by date and category
spec:
  type: parquet_output
  enabled: true
  path: /tmp/worm/lake
  partitionBy:
    - event_date
    - category_name
  maxRowsPerFile: 100000
  compression: zstd
  manifest: true
`;

export const Config: React.FC = () => {
  const [config, setConfig] = useState<RuntimeConfig | null>(null);
  const [connections, setConnections] = useState<ConnectionSummary[]>([]);
  const [selectedYaml, setSelectedYaml] = useState(SAMPLE_HTTP_YAML);
  const [activeTab, setActiveTab] = useState<'connections' | 'system'>('connections');
  const [actionStatus, setActionStatus] = useState<{ type: 'idle' | 'success' | 'error'; message: string }>({
    type: 'idle',
    message: '',
  });
  const [busy, setBusy] = useState(false);

  const fetchConnections = async () => {
    try {
      const res = await apiService.listConnections();
      setConnections(res.connections || []);
    } catch (e) {
      console.error('Failed to load connections', e);
    }
  };

  useEffect(() => {
    apiService.getConfig().then(setConfig).catch(console.error);
    fetchConnections();
  }, []);

  const handleValidate = async () => {
    setBusy(true);
    setActionStatus({ type: 'idle', message: '' });
    try {
      const res = await apiService.validateConnection(selectedYaml);
      if (res.valid) {
        setActionStatus({
          type: 'success',
          message: `Valid connection definition: ${res.name} v${res.version} (${res.type})`,
        });
      } else {
        setActionStatus({ type: 'error', message: res.error || 'Validation failed' });
      }
    } catch (e: any) {
      setActionStatus({ type: 'error', message: e.message });
    } finally {
      setBusy(false);
    }
  };

  const handleTest = async () => {
    setBusy(true);
    setActionStatus({ type: 'idle', message: '' });
    try {
      const res = await apiService.testConnection(selectedYaml);
      setActionStatus({
        type: res.status === 'ok' ? 'success' : 'error',
        message: `Connectivity check: [${res.status.toUpperCase()}] ${res.message} (${res.target})`,
      });
    } catch (e: any) {
      setActionStatus({ type: 'error', message: e.message });
    } finally {
      setBusy(false);
    }
  };

  const handleApply = async () => {
    setBusy(true);
    setActionStatus({ type: 'idle', message: '' });
    try {
      const res = await apiService.applyConnection(selectedYaml);
      setActionStatus({
        type: 'success',
        message: `Applied connection ${res.name} v${res.version}! ${res.restart_required ? '(Note: Listener hot-swap requires process restart)' : ''}`,
      });
      fetchConnections();
    } catch (e: any) {
      setActionStatus({ type: 'error', message: e.message });
    } finally {
      setBusy(false);
    }
  };

  const handleRollback = async () => {
    setBusy(true);
    setActionStatus({ type: 'idle', message: '' });
    try {
      const res = await apiService.rollbackConnection();
      setActionStatus({ type: 'success', message: res.message || 'Connection configuration rolled back.' });
      fetchConnections();
    } catch (e: any) {
      setActionStatus({ type: 'error', message: e.message });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-4 sm:p-5 shadow-sm flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h1 className="text-sm sm:text-base font-mono font-bold text-[#f0f6fc] flex items-center">
            <Settings className="w-4 h-4 mr-2 text-[#58a6ff] shrink-0" />
            System &amp; Connection Configuration
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Declarative Connection resources, multi-sink routing (Kafka, HTTP SIEM, Parquet), and air-gapped system parameters.
          </p>
        </div>

        {/* Tab switch */}
        <div className="flex bg-[#0d1117] p-1 rounded-md border border-[#30363d] text-xs font-mono">
          <button
            onClick={() => setActiveTab('connections')}
            className={`px-3 py-1 rounded transition-colors ${
              activeTab === 'connections'
                ? 'bg-[#21262d] text-[#f0f6fc] font-semibold'
                : 'text-[#8b949e] hover:text-[#c9d1d9]'
            }`}
          >
            Connection Resources ({connections.length})
          </button>
          <button
            onClick={() => setActiveTab('system')}
            className={`px-3 py-1 rounded transition-colors ${
              activeTab === 'system'
                ? 'bg-[#21262d] text-[#f0f6fc] font-semibold'
                : 'text-[#8b949e] hover:text-[#c9d1d9]'
            }`}
          >
            System Parameters
          </button>
        </div>
      </div>

      {activeTab === 'connections' ? (
        <div className="space-y-6">
          {/* Inventory Table */}
          <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-5 shadow-sm space-y-4">
            <div className="flex items-center justify-between pb-3 border-b border-[#21262d]">
              <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] flex items-center">
                <Network className="w-4 h-4 mr-2 text-[#58a6ff]" />
                Active Connections Inventory
              </h2>
              <button
                onClick={handleRollback}
                disabled={busy}
                className="px-2.5 py-1 text-xs font-mono rounded bg-[#21262d] hover:bg-[#30363d] text-[#f85149] border border-[#30363d] flex items-center transition-colors disabled:opacity-50"
                title="Rollback to previous connection snapshot"
              >
                <RotateCcw className="w-3 h-3 mr-1.5" />
                Rollback Snapshot
              </button>
            </div>

            {connections.length === 0 ? (
              <div className="text-xs font-mono text-[#8b949e] py-4 text-center">
                No active connections found in connections directory.
              </div>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-xs font-mono text-left">
                  <thead>
                    <tr className="border-b border-[#21262d] text-[#8b949e]">
                      <th className="pb-2">Name</th>
                      <th className="pb-2">Version</th>
                      <th className="pb-2">Type</th>
                      <th className="pb-2">Status</th>
                      <th className="pb-2">Target Endpoint / Topic</th>
                      <th className="pb-2 text-right">Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-[#21262d]">
                    {connections.map((conn) => (
                      <tr key={conn.name} className="hover:bg-[#0d1117]/50">
                        <td className="py-2.5 font-bold text-[#f0f6fc]">{conn.name}</td>
                        <td className="py-2.5 text-[#8b949e]">{conn.version}</td>
                        <td className="py-2.5">
                          <span className="px-2 py-0.5 rounded text-[11px] bg-[#21262d] text-[#58a6ff] border border-[#30363d]">
                            {conn.type}
                          </span>
                        </td>
                        <td className="py-2.5">
                          {conn.enabled ? (
                            <Badge variant="green">active</Badge>
                          ) : (
                            <Badge variant="amber">disabled</Badge>
                          )}
                        </td>
                        <td className="py-2.5 text-[#c9d1d9] truncate max-w-xs">{conn.target}</td>
                        <td className="py-2.5 text-right">
                          <button
                            onClick={async () => {
                              try {
                                const detail = await apiService.getConnection(conn.name);
                                setSelectedYaml(JSON.stringify(detail, null, 2));
                              } catch (e: any) {
                                setActionStatus({ type: 'error', message: e.message });
                              }
                            }}
                            className="px-2 py-0.5 text-[11px] font-mono rounded bg-[#21262d] text-[#58a6ff] hover:bg-[#30363d] border border-[#30363d]"
                          >
                            Inspect
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          {/* Declarative Editor & Actions */}
          <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-5 shadow-sm space-y-4">
            <div className="flex flex-col sm:flex-row sm:items-center justify-between pb-3 border-b border-[#21262d] gap-2">
              <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc]">
                Declarative Connection Editor (YAML)
              </h2>
              <div className="flex items-center space-x-2">
                <span className="text-[11px] font-mono text-[#8b949e]">Load Sample:</span>
                <button
                  onClick={() => setSelectedYaml(SAMPLE_HTTP_YAML)}
                  className="px-2 py-0.5 text-[11px] font-mono rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
                >
                  HTTP SIEM
                </button>
                <button
                  onClick={() => setSelectedYaml(SAMPLE_KAFKA_YAML)}
                  className="px-2 py-0.5 text-[11px] font-mono rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
                >
                  Kafka Output
                </button>
                <button
                  onClick={() => setSelectedYaml(SAMPLE_PARQUET_YAML)}
                  className="px-2 py-0.5 text-[11px] font-mono rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
                >
                  Parquet Lake
                </button>
              </div>
            </div>

            <textarea
              value={selectedYaml}
              onChange={(e) => setSelectedYaml(e.target.value)}
              rows={12}
              className="w-full bg-[#0d1117] border border-[#30363d] rounded p-3 text-xs font-mono text-[#c9d1d9] focus:outline-none focus:border-[#58a6ff] leading-relaxed resize-y"
              spellCheck={false}
            />

            {/* Action Bar */}
            <div className="flex flex-wrap items-center gap-3">
              <button
                onClick={handleValidate}
                disabled={busy}
                className="px-3 py-1.5 text-xs font-mono font-semibold rounded bg-[#21262d] hover:bg-[#30363d] text-[#c9d1d9] border border-[#30363d] flex items-center transition-colors disabled:opacity-50"
              >
                <Check className="w-3.5 h-3.5 mr-1.5 text-[#58a6ff]" />
                Validate Spec
              </button>

              <button
                onClick={handleTest}
                disabled={busy}
                className="px-3 py-1.5 text-xs font-mono font-semibold rounded bg-[#21262d] hover:bg-[#30363d] text-[#c9d1d9] border border-[#30363d] flex items-center transition-colors disabled:opacity-50"
              >
                <Play className="w-3.5 h-3.5 mr-1.5 text-amber-400" />
                Test Connectivity
              </button>

              <button
                onClick={handleApply}
                disabled={busy}
                className="px-3.5 py-1.5 text-xs font-mono font-semibold rounded bg-[#238636] hover:bg-[#2ea043] text-white flex items-center transition-colors disabled:opacity-50 shadow-sm"
              >
                <ArrowRight className="w-3.5 h-3.5 mr-1.5" />
                Apply Connection
              </button>
            </div>

            {/* Result Banner */}
            {actionStatus.type !== 'idle' && (
              <div
                className={`p-3 rounded-md text-xs font-mono flex items-start gap-2 border ${
                  actionStatus.type === 'success'
                    ? 'bg-[#238636]/10 border-[#238636]/40 text-emerald-400'
                    : 'bg-[#da3633]/10 border-[#da3633]/40 text-red-400'
                }`}
              >
                {actionStatus.type === 'success' ? (
                  <CheckCircle2 className="w-4 h-4 shrink-0 mt-0.5" />
                ) : (
                  <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
                )}
                <div className="flex-1 whitespace-pre-wrap">{actionStatus.message}</div>
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {/* Network & Ingestion Sockets */}
          <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-5 space-y-4 shadow-sm">
            <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-3 border-b border-[#21262d]">
              Network Transport Listeners
            </h2>

            <div className="space-y-3 text-xs font-mono">
              <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                <div className="min-w-0 flex-1">
                  <div className="text-[#f0f6fc] font-semibold truncate">Management Control Plane &amp; UI</div>
                  <div className="text-[11px] text-[#8b949e] truncate mt-0.5">Embedded HTTP REST API &amp; Web UI</div>
                </div>
                <div className="shrink-0">
                  <Badge variant="blue">{config?.ui_address || ':9090'}</Badge>
                </div>
              </div>

              <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                <div className="min-w-0 flex-1">
                  <div className="text-[#f0f6fc] font-semibold truncate">Syslog UDP Listener</div>
                  <div className="text-[11px] text-[#8b949e] truncate mt-0.5">RFC 3164 / 5424 UDP datagram receiver</div>
                </div>
                <div className="shrink-0">
                  <Badge variant="green">{config?.syslog_udp || ':514'}</Badge>
                </div>
              </div>

              <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                <div className="min-w-0 flex-1">
                  <div className="text-[#f0f6fc] font-semibold truncate">Syslog TCP Listener</div>
                  <div className="text-[11px] text-[#8b949e] truncate mt-0.5">Framed &amp; octet-counted TCP streams</div>
                </div>
                <div className="shrink-0">
                  <Badge variant="green">{config?.syslog_tcp || ':514'}</Badge>
                </div>
              </div>

              <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                <div className="min-w-0 flex-1">
                  <div className="text-[#f0f6fc] font-semibold truncate">HTTP Ingestion Webhook</div>
                  <div className="text-[11px] text-[#8b949e] truncate mt-0.5">POST /api/v1/ingest log receiver</div>
                </div>
                <div className="shrink-0">
                  <Badge variant="green">{config?.http_ingest || ':8080'}</Badge>
                </div>
              </div>

              <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                <div className="min-w-0 flex-1">
                  <div className="text-[#f0f6fc] font-semibold truncate">Spool Inbox Directory</div>
                  <div className="text-[11px] text-[#8b949e] truncate mt-0.5">Monitored atomic batch file drop directory</div>
                </div>
                <div className="shrink-0">
                  <Badge variant="amber">{config?.inbox_dir || 'data/inbox'}</Badge>
                </div>
              </div>
            </div>
          </div>

          {/* Engine & Durability Settings */}
          <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-5 space-y-4 min-w-0 shadow-sm flex flex-col justify-between">
            <div>
              <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-3 border-b border-[#21262d]">
                Engine Durability &amp; Worker Settings
              </h2>

              <div className="space-y-3 text-xs font-mono mt-4">
                <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                  <div className="min-w-0 flex-1">
                    <div className="text-[#f0f6fc] font-semibold truncate">SQLite Raw &amp; Quarantine Store</div>
                    <div className="text-[11px] text-[#8b949e] truncate mt-0.5">WAL mode, synchronous=NORMAL, cryptographic store</div>
                  </div>
                  <span className="text-[#c9d1d9] font-mono min-w-0 max-w-[50%] text-[11px] sm:text-xs truncate bg-[#161b22] px-2.5 py-1 rounded border border-[#30363d]">
                    {config?.db_path || 'data/worm.db'}
                  </span>
                </div>

                <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                  <div className="min-w-0 flex-1">
                    <div className="text-[#f0f6fc] font-semibold truncate">Concurrent Pipeline Workers</div>
                    <div className="text-[11px] text-[#8b949e] truncate mt-0.5">Goroutine worker pool size</div>
                  </div>
                  <div className="shrink-0">
                    <Badge variant="blue">{config?.workers || 4} workers</Badge>
                  </div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-md bg-[#0d1117] border border-[#21262d] gap-3 min-w-0">
                  <div className="min-w-0 flex-1">
                    <div className="text-[#f0f6fc] font-semibold truncate">WORM Release Version</div>
                    <div className="text-[11px] text-[#8b949e] truncate mt-0.5">SIH 26156 reference core</div>
                  </div>
                  <div className="shrink-0">
                    <Badge variant="purple">v{config?.version || '1.0.4'}</Badge>
                  </div>
                </div>
              </div>
            </div>

            {/* Air-Gap Guarantee Certificate */}
            <div className="bg-[#0d1117] border border-purple-900/60 rounded-md p-4 mt-4">
              <div className="flex items-center space-x-2 text-purple-300 font-mono text-xs font-bold mb-2.5">
                <ShieldCheck className="w-4 h-4 text-purple-400 shrink-0" />
                <span>Air-Gapped Operational Compliance</span>
              </div>
              <ul className="text-[11px] font-mono text-[#8b949e] space-y-2">
                <li className="flex items-center text-emerald-400">
                  <CheckCircle2 className="w-3.5 h-3.5 mr-2 shrink-0" />
                  Zero external CDN links (no remote scripts, fonts, or stylesheets)
                </li>
                <li className="flex items-center text-emerald-400">
                  <CheckCircle2 className="w-3.5 h-3.5 mr-2 shrink-0" />
                  Zero runtime Node.js/npm dependencies (compiled into static Go binary)
                </li>
                <li className="flex items-center text-emerald-400">
                  <CheckCircle2 className="w-3.5 h-3.5 mr-2 shrink-0" />
                  Pure Go SQLite driver (CGO-free ModernC SQLite)
                </li>
                <li className="flex items-center text-emerald-400">
                  <CheckCircle2 className="w-3.5 h-3.5 mr-2 shrink-0" />
                  Strict isolation: Log Ingest Plane (:8080) separated from Control Plane (:9090)
                </li>
              </ul>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
