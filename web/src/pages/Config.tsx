import React, { useEffect, useState } from 'react';
import { Settings, ShieldCheck, CheckCircle2 } from 'lucide-react';
import { RuntimeConfig } from '../types/config';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';

export const Config: React.FC = () => {
  const [config, setConfig] = useState<RuntimeConfig | null>(null);

  useEffect(() => {
    apiService.getConfig().then(setConfig).catch(console.error);
  }, []);

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4">
        <h1 className="text-sm font-mono font-bold text-[#f0f6fc] flex items-center">
          <Settings className="w-4 h-4 mr-2 text-[#58a6ff]" />
          System & Pipeline Configuration
        </h1>
        <p className="text-xs font-mono text-[#8b949e] mt-1">
          Active operational parameters, multi-transport socket bindings, and air-gapped environment verification.
        </p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">

        {/* Network & Ingestion Sockets */}
        <div className="bg-[#161b22] border border-[#30363d] rounded-md p-5 space-y-4">
          <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-2 border-b border-[#21262d]">
            Network Transport Listeners
          </h2>

          <div className="space-y-3 text-xs font-mono">
            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">Management Control Plane & UI</div>
                <div className="text-[11px] text-[#8b949e] truncate">Embedded HTTP REST API & Web UI</div>
              </div>
              <div className="shrink-0">
                <Badge variant="blue">{config?.ui_address || ':9090'}</Badge>
              </div>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">Syslog UDP Listener</div>
                <div className="text-[11px] text-[#8b949e] truncate">RFC 3164 / 5424 UDP datagram receiver</div>
              </div>
              <div className="shrink-0">
                <Badge variant="green">{config?.syslog_udp || ':514'}</Badge>
              </div>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">Syslog TCP Listener</div>
                <div className="text-[11px] text-[#8b949e] truncate">Framed & octet-counted TCP streams</div>
              </div>
              <div className="shrink-0">
                <Badge variant="green">{config?.syslog_tcp || ':514'}</Badge>
              </div>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">HTTP Ingestion Webhook</div>
                <div className="text-[11px] text-[#8b949e] truncate">POST /api/v1/ingest log receiver</div>
              </div>
              <div className="shrink-0">
                <Badge variant="green">{config?.http_ingest || ':8080'}</Badge>
              </div>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">Spool Inbox Directory</div>
                <div className="text-[11px] text-[#8b949e] truncate">Monitored atomic batch file drop directory</div>
              </div>
              <div className="shrink-0">
                <Badge variant="amber">{config?.inbox_dir || 'data/inbox'}</Badge>
              </div>
            </div>
          </div>
        </div>

        {/* Engine & Durability Settings */}
        <div className="bg-[#161b22] border border-[#30363d] rounded-md p-5 space-y-4 min-w-0">
          <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-2 border-b border-[#21262d]">
            Engine Durability & Worker Settings
          </h2>

          <div className="space-y-3 text-xs font-mono">
            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">SQLite Raw & Quarantine Store</div>
                <div className="text-[11px] text-[#8b949e] truncate">WAL mode, synchronous=NORMAL, cryptographic store</div>
              </div>
              <span className="text-[#c9d1d9] font-mono shrink-0 text-[11px] sm:text-xs truncate">{config?.db_path || 'data/worm.db'}</span>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">Concurrent Pipeline Workers</div>
                <div className="text-[11px] text-[#8b949e] truncate">Goroutine worker pool size</div>
              </div>
              <div className="shrink-0">
                <Badge variant="blue">{config?.workers || 4} workers</Badge>
              </div>
            </div>

            <div className="flex items-center justify-between p-2 rounded bg-[#0d1117] border border-[#21262d] gap-2 min-w-0">
              <div className="min-w-0 flex-1">
                <div className="text-[#f0f6fc] font-semibold truncate">WORM Release Version</div>
                <div className="text-[11px] text-[#8b949e] truncate">SIH 26156 reference core</div>
              </div>
              <div className="shrink-0">
                <Badge variant="purple">v{config?.version || '1.0.0'}</Badge>
              </div>
            </div>
          </div>

          {/* Air-Gap Guarantee Certificate */}
          <div className="bg-[#0d1117] border border-purple-900/60 rounded-md p-4 mt-4">
            <div className="flex items-center space-x-2 text-purple-300 font-mono text-xs font-bold mb-2">
              <ShieldCheck className="w-4 h-4 text-purple-400" />
              Air-Gapped Operational Compliance
            </div>
            <ul className="text-[11px] font-mono text-[#8b949e] space-y-1.5">
              <li className="flex items-center text-emerald-400">
                <CheckCircle2 className="w-3.5 h-3.5 mr-1.5 shrink-0" />
                Zero external CDN links (no remote scripts, fonts, or stylesheets)
              </li>
              <li className="flex items-center text-emerald-400">
                <CheckCircle2 className="w-3.5 h-3.5 mr-1.5 shrink-0" />
                Zero runtime Node.js/npm dependencies (compiled into static Go binary)
              </li>
              <li className="flex items-center text-emerald-400">
                <CheckCircle2 className="w-3.5 h-3.5 mr-1.5 shrink-0" />
                Pure Go SQLite driver (CGO-free ModernC SQLite)
              </li>
              <li className="flex items-center text-emerald-400">
                <CheckCircle2 className="w-3.5 h-3.5 mr-1.5 shrink-0" />
                Strict isolation: Log Ingest Plane (:8080) separated from Control Plane (:9090)
              </li>
            </ul>
          </div>
        </div>

      </div>
    </div>
  );
};
