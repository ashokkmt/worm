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
  stats?: SystemStats | null;
  onRefreshStats?: () => void;
}

export const Dashboard: React.FC<DashboardProps> = ({ onNavigate, stats: propStats, onRefreshStats }) => {
  const [internalStats, setInternalStats] = useState<SystemStats | null>(null);
  const [sources, setSources] = useState<SourceSummary[]>([]);
  const [loading, setLoading] = useState<boolean>(false);
  const [lastRefreshed, setLastRefreshed] = useState<Date>(new Date());

  const stats = propStats !== undefined ? propStats : internalStats;

  const fetchSources = async () => {
    try {
      const sourcesData = await apiService.listSources();
      const sorted = [...(sourcesData.sources || [])].sort((a, b) => {
        const timeA = a.last_seen ? new Date(a.last_seen).getTime() : 0;
        const timeB = b.last_seen ? new Date(b.last_seen).getTime() : 0;
        return timeB - timeA;
      });
      setSources(sorted.slice(0, 10));
      setLastRefreshed(new Date());
    } catch (e) {
      console.error('Failed to load sources telemetry', e);
    }
  };

  const handleRefresh = async () => {
    setLoading(true);
    try {
      if (onRefreshStats) {
        onRefreshStats();
      } else {
        const statsData = await apiService.getStats();
        setInternalStats(statsData);
      }
      await fetchSources();
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchSources();
    if (propStats === undefined) {
      apiService.getStats().then(setInternalStats).catch(() => { });
    }
    const interval = setInterval(() => {
      fetchSources();
      if (propStats === undefined) {
        apiService.getStats().then(setInternalStats).catch(() => { });
      }
    }, 5000);
    return () => clearInterval(interval);
  }, [propStats]);

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
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 pb-2 border-b border-[#21262d]">
        <div>
          <h1 className="text-xl font-mono font-bold text-[#f0f6fc] tracking-tight flex items-center">
            System Telemetry &amp; Loss Ledger
          </h1>
          <p className="text-xs font-mono text-[#8b949e] mt-1">
            Real-time pipeline accounting, cryptographic verification, and air-gapped stream metrics.
          </p>
        </div>
        <div className="flex items-center space-x-3 shrink-0">
          <span className="text-xs font-mono text-[#8b949e] bg-[#161b22] px-2.5 py-1 rounded-md border border-[#30363d]">
            Updated: <span className="text-[#c9d1d9] font-medium">{lastRefreshed.toLocaleTimeString()}</span>
          </span>
          <button
            onClick={handleRefresh}
            className="p-1.5 rounded-md bg-[#21262d] hover:bg-[#30363d] text-[#8b949e] hover:text-[#c9d1d9] border border-[#30363d] transition-colors"
            title="Refresh now"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin text-[#58a6ff]' : ''}`} />
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
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6 min-w-0">

        {/* Active Sources Table */}
        <div className="lg:col-span-2 bg-[#161b22] border border-[#30363d] rounded-lg p-4 sm:p-5 min-w-0 shadow-sm">
          <div className="flex items-center justify-between pb-3 border-b border-[#21262d] gap-2 min-w-0">
            <div className="flex items-center space-x-2 min-w-0">
              <Server className="w-4 h-4 text-[#58a6ff] shrink-0" />
              <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] truncate">
                Ingestion Sources Inventory
              </h2>
            </div>
            <span className="text-xs font-mono text-[#8b949e] shrink-0">
              {sources.length > 0 ? `Top ${sources.length} latest source feeds` : '0 active source feeds'}
            </span>
          </div>

          <div className="overflow-x-auto mt-3 max-w-full">
            <table className="w-full text-left text-xs font-mono">
              <thead>
                <tr className="text-[#8b949e] border-b border-[#21262d] bg-[#0d1117]">
                  <th className="py-2.5 px-3 font-medium whitespace-nowrap">Category</th>
                  <th className="py-2.5 px-3 font-medium whitespace-nowrap">Source ID / IP</th>
                  <th className="py-2.5 px-3 font-medium text-right whitespace-nowrap">Event Count</th>
                  <th className="py-2.5 px-3 font-medium text-right whitespace-nowrap">Last Seen</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[#21262d]">
                {sources.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="py-8 text-center text-[#8b949e]">
                      No events ingested yet. Waiting for stream input on UDP/TCP/HTTP.
                    </td>
                  </tr>
                ) : (
                  sources.map((src, idx) => (
                    <tr key={idx} className="hover:bg-[#21262d]/60 transition-colors">
                      <td className="py-2.5 px-3 whitespace-nowrap">
                        <Badge variant="blue">{src.category}</Badge>
                      </td>
                      <td className="py-2.5 px-3 font-semibold text-[#f0f6fc] whitespace-nowrap">
                        {src.source_id}
                      </td>
                      <td className="py-2.5 px-3 text-right text-emerald-400 font-semibold whitespace-nowrap">
                        {src.events.toLocaleString()}
                      </td>
                      <td className="py-2.5 px-3 text-right text-[#8b949e] whitespace-nowrap">
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
        <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-4 sm:p-5 flex flex-col justify-between space-y-4 shadow-sm">
          <div>
            <h2 className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc] pb-3 border-b border-[#21262d]">
              Operational Quick Actions
            </h2>
            <div className="mt-4 space-y-2.5">
              <button
                onClick={() => onNavigate('events')}
                className="w-full flex items-center justify-between p-3 rounded-md bg-[#0d1117] hover:bg-[#21262d] border border-[#21262d] hover:border-[#30363d] text-xs font-mono text-[#c9d1d9] transition-all duration-150 group"
              >
                <span>Browse Normalized Events</span>
                <ArrowRight className="w-3.5 h-3.5 text-[#58a6ff] group-hover:translate-x-0.5 transition-transform" />
              </button>
              <button
                onClick={() => onNavigate('quarantine')}
                className="w-full flex items-center justify-between p-3 rounded-md bg-[#0d1117] hover:bg-[#21262d] border border-[#21262d] hover:border-[#30363d] text-xs font-mono text-[#c9d1d9] transition-all duration-150 group"
              >
                <span>Inspect Quarantine Dead-Letter Queue</span>
                <ArrowRight className="w-3.5 h-3.5 text-amber-400 group-hover:translate-x-0.5 transition-transform" />
              </button>
              <button
                onClick={() => onNavigate('onboard')}
                className="w-full flex items-center justify-between p-3 rounded-md bg-[#0d1117] hover:bg-[#21262d] border border-[#21262d] hover:border-[#30363d] text-xs font-mono text-[#c9d1d9] transition-all duration-150 group"
              >
                <span>Onboard New Source / Test Parser</span>
                <ArrowRight className="w-3.5 h-3.5 text-emerald-400 group-hover:translate-x-0.5 transition-transform" />
              </button>
            </div>
          </div>

          <div className="bg-[#0d1117] border border-[#30363d] rounded-md p-3.5 text-xs font-mono">
            <div className="text-[#8b949e] font-semibold mb-2 text-[11px] uppercase tracking-wider">Security &amp; Air-Gap Profile</div>
            <div className="text-[11px] text-emerald-400 space-y-1.5 font-medium">
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
