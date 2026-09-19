import React from 'react';
import { ShieldCheck, AlertCircle } from 'lucide-react';
import { LossAudit } from '../types/telemetry';

interface LossLedgerProps {
  accepted: number;
  normalized: number;
  quarantined: number;
  pending: number;
  delivered: number;
  audit?: LossAudit;
}

export const LossLedger: React.FC<LossLedgerProps> = ({
  accepted,
  normalized,
  quarantined,
  pending,
  delivered,
  audit,
}) => {
  const normPct = accepted > 0 ? ((normalized / accepted) * 100).toFixed(1) : '0.0';
  const quarPct = accepted > 0 ? ((quarantined / accepted) * 100).toFixed(1) : '0.0';
  const pendPct = accepted > 0 ? ((pending / accepted) * 100).toFixed(1) : '0.0';

  const isValid = audit ? audit.valid : (accepted === normalized + quarantined + pending);
  const auditReason = audit ? audit.reason : 'invariant verified';

  return (
    <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-4 sm:p-5 shadow-sm space-y-4">
      {/* Header & Status Pill */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between pb-3 border-b border-[#21262d] gap-2">
        <div className="flex items-center space-x-2 flex-wrap min-w-0">
          <span className="text-xs font-mono font-bold uppercase tracking-wider text-[#f0f6fc]">
            Loss Accounting Ledger (Strict Invariant Audit)
          </span>
          <span className="text-[10px] font-mono text-[#8b949e] px-2 py-0.5 rounded bg-[#0d1117] border border-[#30363d]">
            Accepted = Normalized + Quarantined + Pending
          </span>
        </div>
        <div className="flex items-center space-x-2 shrink-0">
          {isValid ? (
            <span className="flex items-center text-xs font-mono font-medium text-emerald-400 bg-emerald-950/70 px-2.5 py-1 rounded-full border border-emerald-800/80 shadow-xs">
              <ShieldCheck className="w-3.5 h-3.5 mr-1.5 shrink-0 text-emerald-400" />
              Audit Passed: Zero-Loss Guaranteed
            </span>
          ) : (
            <span className="flex items-center text-xs font-mono font-medium text-rose-400 bg-rose-950/70 px-2.5 py-1 rounded-full border border-rose-800/80 shadow-xs">
              <AlertCircle className="w-3.5 h-3.5 mr-1.5 shrink-0 text-rose-400" />
              Invariant Discrepancy Detected
            </span>
          )}
        </div>
      </div>

      {/* Grid of 5 counters */}
      <div className="grid grid-cols-2 sm:grid-cols-5 gap-3">
        <div className="bg-[#0d1117] p-3 rounded-md border border-[#21262d] transition-colors hover:border-[#30363d]">
          <div className="text-[11px] font-mono text-[#8b949e] uppercase font-semibold">Accepted Ingest</div>
          <div className="text-xl font-mono font-bold text-[#f0f6fc] mt-1 tracking-tight">{accepted.toLocaleString()}</div>
          <div className="text-[10px] font-mono text-[#8b949e] mt-0.5">100% total ingress</div>
        </div>

        <div className="bg-[#0d1117] p-3 rounded-md border border-[#21262d] transition-colors hover:border-emerald-900/60">
          <div className="text-[11px] font-mono text-emerald-400 uppercase font-semibold">Normalized</div>
          <div className="text-xl font-mono font-bold text-emerald-400 mt-1 tracking-tight">{normalized.toLocaleString()}</div>
          <div className="text-[10px] font-mono text-[#8b949e] mt-0.5">{normPct}% of total</div>
        </div>

        <div className="bg-[#0d1117] p-3 rounded-md border border-[#21262d] transition-colors hover:border-rose-900/60">
          <div className="text-[11px] font-mono text-rose-400 uppercase font-semibold">Quarantined (DLQ)</div>
          <div className="text-xl font-mono font-bold text-rose-400 mt-1 tracking-tight">{quarantined.toLocaleString()}</div>
          <div className="text-[10px] font-mono text-[#8b949e] mt-0.5">{quarPct}% of total</div>
        </div>

        <div className="bg-[#0d1117] p-3 rounded-md border border-[#21262d] transition-colors hover:border-amber-900/60">
          <div className="text-[11px] font-mono text-amber-400 uppercase font-semibold">Pending In Buffer</div>
          <div className="text-xl font-mono font-bold text-amber-400 mt-1 tracking-tight">{pending.toLocaleString()}</div>
          <div className="text-[10px] font-mono text-[#8b949e] mt-0.5">{pendPct}% in-flight</div>
        </div>

        <div className="bg-[#0d1117] p-3 rounded-md border border-[#21262d] transition-colors hover:border-blue-900/60">
          <div className="text-[11px] font-mono text-blue-400 uppercase font-semibold">Delivered Out</div>
          <div className="text-xl font-mono font-bold text-blue-400 mt-1 tracking-tight">{delivered.toLocaleString()}</div>
          <div className="text-[10px] font-mono text-[#8b949e] mt-0.5">sinks emitted</div>
        </div>
      </div>

      {/* Segmented Audit Bar */}
      <div className="space-y-1.5">
        <div className="w-full bg-[#0d1117] h-2.5 rounded-full overflow-hidden flex border border-[#21262d] p-0.5">
          <div
            style={{ width: `${normPct}%` }}
            className="bg-emerald-500 rounded-l-full transition-all duration-300 shadow-xs"
            title={`Normalized: ${normPct}%`}
          />
          <div
            style={{ width: `${quarPct}%` }}
            className="bg-rose-500 transition-all duration-300 shadow-xs"
            title={`Quarantined: ${quarPct}%`}
          />
          <div
            style={{ width: `${pendPct}%` }}
            className="bg-amber-500 rounded-r-full transition-all duration-300 shadow-xs"
            title={`Pending: ${pendPct}%`}
          />
        </div>

        <div className="text-[11px] font-mono text-[#8b949e] flex flex-col sm:flex-row justify-between items-start sm:items-center gap-1 pt-1">
          <span>Reason: <span className="text-[#c9d1d9] font-medium">{auditReason}</span></span>
          <span className="font-medium">
            {isValid
              ? <span className="text-emerald-400">Loss rate: 0.000000% (No dropped datagrams)</span>
              : <span className="text-rose-400">Loss rate: AUDIT FAILED — {auditReason}</span>}
          </span>
        </div>
      </div>
    </div>
  );
};
