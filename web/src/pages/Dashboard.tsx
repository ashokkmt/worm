import React, { useEffect, useState } from 'react';
import { Activity, Server, Cpu, Clock, RefreshCw, AlertTriangle, ArrowRight } from 'lucide-react';
import { MetricCard } from '../components/MetricCard';
import { LossLedger } from '../components/LossLedger';
import { Badge } from '../components/Badge';
import { SystemStats } from '../types/telemetry';
import { SourceSummary } from '../types/event';
import { apiService } from '../services/api';

interface DashboardProps {
  onNavigate: (tab: string, param?: string) => void;
}

export const Dashboard: React.FC<DashboardProps> = ({ onNavigate }) => {
  const [stats, setStats] = useState<SystemStats | null>(null);
  const [sources, setSources] = useState<SourceSummary[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [lastRefreshed, setLastRefreshed] = useState<Date>(new Date());

  const fetchTelemetry = async () => {
    try {
      const [statsData, sourcesData] = await Promise.all([
        apiService.getStats(),
        apiService.listSources(),
      ]);
      setStats(statsData);
      setSources(sourcesData.sources);
      setLastRefreshed(new Date());
    } catch (e) {
      console.error('Failed to load telemetry', e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchTelemetry();
    const interval = setInterval(fetchTelemetry, 2000);
    return () => clearInterval(interval);
  }, []);

  const formatUptime = (seconds: number) => {
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m ${s}s`;
    return `${m}m ${s}s`;
  };

  const formatBytes = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  };

  return (
    <div className="space-y-6">
      {/* Top Header */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-mono font-bold text-[#f0f6fc] tracking-tight">
            System Telemetry & Loss Ledger
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Real-time pipeline accounting, cryptographic verification, and air-gapped stream metrics.
          </p>
        </div>
        <div className="flex items-center space-x-3">
          <span className="text-xs font-mono text-[#8b949e]">
            Updated: {lastRefreshed.toLocaleTimeString()}
          </span>
          <button
            onClick={fetchTelemetry}
            className="p-1.5 rounded bg-[#21262d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d]"
            title="Refresh now"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* KPI Metric Cards */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <MetricCard
          label="Throughput (EPS)"
          value={stats ? stats.eps.toFixed(1) : '0.0'}
          subValue="events per second"
          status="good"
          icon={<Activity className="w-4 h-4 text-emerald-400" />}
        />
        <MetricCard
          label="Quarantined (DLQ)"
          value={stats ? stats.quarantined : 0}
          subValue={stats && stats.quarantined > 0 ? 'Requires attention' : 'Clean stream'}
          status={stats && stats.quarantined > 0 ? 'warning' : 'good'}
          icon={<AlertTriangle className="w-4 h-4 text-amber-400" />}
        />
        <MetricCard
          label="Memory (Heap Alloc)"
          value={stats ? formatBytes(stats.memory_bytes) : '0 MB'}
          subValue={`Goroutines: ${stats ? stats.goroutines : 0}`}
          status="normal"
          icon={<Cpu className="w-4 h-4 text-[#58a6ff]" />}
        />
        <MetricCard
          label="Uptime"
          value={stats ? formatUptime(stats.uptime_seconds) : '0s'}
          subValue="WORM engine active"
          status="normal"
          icon={<Clock className="w-4 h-4 text-[#8b949e]" />}
        />
      </div>

      {/* Loss Accounting Ledger */}
      <LossLedger
        accepted={stats?.accepted || 0}
        normalized={stats?.normalized || 0}
        quarantined={stats?.quarantined || 0}
        pending={stats?.pending || 0}
        delivered={stats?.delivered || 0}
        audit={stats?.loss_audit}
      />

      {/* Two Column Layout: Sources Inventory & Quick Actions */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        
        {/* Active Sources Table */}
        <div className="lg:col-span-2 bg-[#161b22] border border-[#30363d] rounded-md p-4">
          <div className="flex items-center justify-between pb-3 border-b border-[#21262d]">
            <div className="flex items-center space-x-2">
              <Server className="w-4 h-4 text-[#58a6ff]" />
              <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc]">
                Ingestion Sources Inventory
              </h2>
            </div>
            <span className="text-xs font-mono text-[#8b949e]">
              {sources.length} active source feeds
            </span>
          </div>

          <div className="overflow-x-auto mt-3">
            <table className="w-full text-left text-xs font-mono">
              <thead>
                <tr className="text-[#8b949e] border-b border-[#21262d]">
                  <th className="py-2 px-2 font-medium">Category</th>
                  <th className="py-2 px-2 font-medium">Source ID / IP</th>
                  <th className="py-2 px-2 font-medium text-right">Event Count</th>
                  <th className="py-2 px-2 font-medium text-right">Last Seen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#21262d]">
                {sources.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="py-6 text-center text-[#8b949e]">
                      No events ingested yet. Waiting for stream input on UDP/TCP/HTTP.
                    </td>
                  </tr>
                ) : (
                  sources.map((src, idx) => (
                    <tr key={idx} className="hover:bg-[#21262d]/50 transition-colors">
                      <td className="py-2 px-2">
                        <Badge variant="blue">{src.category}</Badge>
                      </td>
                      <td className="py-2 px-2 font-semibold text-[#f0f6fc]">
                        {src.source_id}
                      </td>
                      <td className="py-2 px-2 text-right text-emerald-400 font-semibold">
                        {src.events.toLocaleString()}
                      </td>
                      <td className="py-2 px-2 text-right text-[#8b949e]">
                        {src.last_seen ? new Date(src.last_seen).toLocaleTimeString() : 'N/A'}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>

        {/* Quick Operations & Air-Gap Checklist */}
        <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4 flex flex-col justify-between space-y-4">
          <div>
            <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-3 border-b border-[#21262d]">
              Operational Quick Actions
            </h2>
            <div className="mt-3 space-y-2">
              <button
                onClick={() => onNavigate('events')}
                className="w-full flex items-center justify-between p-2.5 rounded bg-[#21262d] hover:bg-[#30363d] text-xs font-mono text-[#c9d1d9] transition"
              >
                <span>Browse Normalized Events</span>
                <ArrowRight className="w-3.5 h-3.5 text-[#58a6ff]" />
              </button>
              <button
                onClick={() => onNavigate('quarantine')}
                className="w-full flex items-center justify-between p-2.5 rounded bg-[#21262d] hover:bg-[#30363d] text-xs font-mono text-[#c9d1d9] transition"
              >
                <span>Inspect Quarantine Dead-Letter Queue</span>
                <ArrowRight className="w-3.5 h-3.5 text-amber-400" />
              </button>
              <button
                onClick={() => onNavigate('onboard')}
                className="w-full flex items-center justify-between p-2.5 rounded bg-[#21262d] hover:bg-[#30363d] text-xs font-mono text-[#c9d1d9] transition"
              >
                <span>Onboard New Source / Test Parser</span>
                <ArrowRight className="w-3.5 h-3.5 text-emerald-400" />
              </button>
            </div>
          </div>

          <div className="bg-[#0d1117] border border-[#30363d] rounded p-3 text-xs font-mono">
            <div className="text-[#8b949e] font-semibold mb-1">Security & Air-Gap Profile</div>
            <div className="text-[11px] text-emerald-400 space-y-1">
              <div>✓ Zero External CDN Requests</div>
              <div>✓ Self-Contained Go Binary (go:embed)</div>
              <div>✓ SQLite WAL Commit Durability</div>
              <div>✓ Cryptographic SHA-256 Verification</div>
            </div>
          </div>
        </div>

      </div>
    </div>
  );
};
