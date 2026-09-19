import React, { useEffect, useState } from 'react';
import { AlertTriangle, Play, RefreshCw, CheckCircle2, ChevronLeft, ChevronRight, FileCode, X } from 'lucide-react';
import { QuarantineEntry } from '../types/event';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';

export const Quarantine: React.FC = () => {
  const [entries, setEntries] = useState<QuarantineEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [limit, setLimit] = useState(25);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [replayingId, setReplayingId] = useState<string | null>(null);
  const [replayingAll, setReplayingAll] = useState(false);
  const [replaySuccess, setReplaySuccess] = useState<string | null>(null);
  const [selectedPreview, setSelectedPreview] = useState<QuarantineEntry | null>(null);

  const fetchQuarantine = async () => {
    setLoading(true);
    try {
      const data = await apiService.listQuarantine(limit, offset);
      setEntries(data.quarantine);
      setTotal(data.total);
    } catch (e) {
      console.error('Failed to load quarantine entries', e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchQuarantine();
  }, [limit, offset]);

  const handleReplay = async (id: string) => {
    setReplayingId(id);
    setReplaySuccess(null);
    try {
      const res = await apiService.replayQuarantine(id);
      setReplaySuccess(`Successfully reprocessed and emitted event ${res.event?.worm?.event_id || id}`);
      await fetchQuarantine();
    } catch (e: any) {
      alert('Replay failed: ' + e.message);
    } finally {
      setReplayingId(null);
    }
  };

  const handleReplayAll = async () => {
    setReplayingAll(true);
    setReplaySuccess(null);
    try {
      const res = await apiService.replayAllQuarantine();
      if (res.total === 0) {
        setReplaySuccess('No eligible quarantine records found to replay.');
      } else {
        setReplaySuccess(`Bulk Replay Complete: ${res.replayed} reprocessed successfully, ${res.failed} failed out of ${res.total} total.`);
      }
      await fetchQuarantine();
    } catch (e: any) {
      alert('Bulk replay failed: ' + e.message);
    } finally {
      setReplayingAll(false);
    }
  };

  return (
    <div className="space-y-4">
      {/* Header Bar */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-lg p-4 sm:p-5 shadow-sm">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div>
            <h1 className="text-sm sm:text-base font-mono font-bold text-[#f0f6fc] flex items-center">
              <AlertTriangle className="w-4 h-4 mr-2 text-rose-400 shrink-0" />
              Quarantine Dead-Letter Queue (DLQ)
            </h1>
            <p className="text-xs font-mono text-[#8b949e] mt-1">
              Events diverted due to decode failures, unknown schemas, or match errors. Preserved with zero data loss.
            </p>
          </div>
          <div className="flex items-center gap-2 sm:gap-3 shrink-0 self-start sm:self-auto flex-wrap">
            <span className="text-xs font-mono text-[#8b949e] bg-[#0d1117] px-2.5 py-1 rounded-md border border-[#30363d]">
              Showing <span className="text-[#f0f6fc] font-semibold">{entries.length}</span> of <span className="text-[#f0f6fc] font-semibold">{total.toLocaleString()}</span>
            </span>
            <button
              onClick={fetchQuarantine}
              className="px-3 py-1.5 bg-[#21262d] hover:bg-[#30363d] text-xs font-mono font-medium text-[#c9d1d9] hover:text-white rounded-md border border-[#30363d] flex items-center transition-colors shadow-xs"
            >
              <RefreshCw className={`w-3.5 h-3.5 mr-1.5 ${loading ? 'animate-spin text-[#58a6ff]' : ''}`} />
              Refresh Queue
            </button>
            <button
              disabled={replayingAll || entries.length === 0}
              onClick={handleReplayAll}
              className="px-3.5 py-1.5 bg-[#238636] hover:bg-[#2ea043] disabled:opacity-50 text-xs font-mono text-white rounded-md font-medium flex items-center transition-colors shadow-xs"
              title="Initiate replay for all eligible quarantined records"
            >
              <Play className={`w-3.5 h-3.5 mr-1.5 ${replayingAll ? 'animate-spin' : ''}`} />
              {replayingAll ? 'Replaying All...' : 'Replay All'}
            </button>
          </div>
        </div>

        {replaySuccess && (
          <div className="mt-4 p-3 bg-emerald-950/70 border border-emerald-800/80 text-emerald-300 rounded-md text-xs font-mono flex items-center shadow-xs">
            <CheckCircle2 className="w-4 h-4 mr-2 shrink-0 text-emerald-400" />
            {replaySuccess}
          </div>
        )}
      </div>

      {/* Quarantine Table */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-lg overflow-hidden shadow-sm">
        <div className="overflow-x-auto max-w-full">
          <table className="w-full text-left text-xs font-mono">
            <thead>
              <tr className="bg-[#0d1117] text-[#8b949e] border-b border-[#21262d]">
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Quarantine ID</th>
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Stage</th>
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Reason</th>
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Raw Preview</th>
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Quarantined At</th>
                <th className="py-3 px-3.5 font-medium whitespace-nowrap">Status</th>
                <th className="py-3 px-3.5 font-medium text-right whitespace-nowrap">Action</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#21262d]">
              {entries.length === 0 ? (
                <tr>
                  <td colSpan={7} className="py-12 text-center text-[#8b949e]">
                    Quarantine queue is empty. All ingested events successfully parsed and delivered.
                  </td>
                </tr>
              ) : (
                entries.map((item) => (
                  <tr key={item.quarantine_id} className="hover:bg-[#21262d]/60 transition-colors">
                    <td className="py-2.5 px-3.5 font-semibold text-rose-400 whitespace-nowrap">
                      {item.quarantine_id}
                    </td>
                    <td className="py-2.5 px-3.5 whitespace-nowrap">
                      <Badge variant="amber">{item.stage}</Badge>
                    </td>
                    <td className="py-2.5 px-3.5 text-[#f0f6fc] font-medium whitespace-nowrap">
                      {item.reason}
                    </td>
                    <td className="py-2.5 px-3.5 max-w-[200px] truncate text-[#8b949e] whitespace-nowrap" title={item.raw_preview}>
                      {item.raw_preview}
                    </td>
                    <td className="py-2.5 px-3.5 text-[#8b949e] whitespace-nowrap">
                      {item.quarantined_at ? new Date(item.quarantined_at).toLocaleTimeString() : 'N/A'}
                    </td>
                    <td className="py-2.5 px-3.5 whitespace-nowrap">
                      {item.replayed_at ? (
                        <Badge variant="green">Replayed</Badge>
                      ) : (
                        <Badge variant="red">Pending DLQ</Badge>
                      )}
                    </td>
                    <td className="py-2.5 px-3.5 text-right space-x-2 whitespace-nowrap">
                      <button
                        onClick={() => setSelectedPreview(item)}
                        className="px-2.5 py-1 rounded-md bg-[#0d1117] hover:bg-[#21262d] text-[#c9d1d9] hover:text-white border border-[#30363d] inline-flex items-center text-[11px] font-medium transition-colors shadow-xs"
                      >
                        <FileCode className="w-3.5 h-3.5 mr-1" />
                        Detail
                      </button>
                      <button
                        disabled={replayingId === item.quarantine_id || !item.replay_eligible || !!item.replayed_at}
                        onClick={() => handleReplay(item.quarantine_id)}
                        className="px-3 py-1 rounded-md bg-[#238636] hover:bg-[#2ea043] text-white inline-flex items-center text-[11px] font-medium disabled:opacity-50 transition-colors shadow-xs"
                        title={!item.replay_eligible ? 'Not eligible for replay' : 'Replay event'}
                      >
                        <Play className="w-3.5 h-3.5 mr-1" />
                        {replayingId === item.quarantine_id ? 'Replaying...' : 'Replay'}
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination Bar */}
        <div className="bg-[#0d1117] px-4 py-3 border-t border-[#21262d] flex flex-col sm:flex-row items-center justify-between text-xs font-mono text-[#8b949e] gap-2">
          <div className="flex items-center space-x-2 flex-wrap">
            <span>Page size:</span>
            <select
              value={limit}
              onChange={(e) => {
                setLimit(Number(e.target.value));
                setOffset(0);
              }}
              className="bg-[#161b22] border border-[#30363d] rounded-md text-xs px-2.5 py-1 text-[#c9d1d9] focus:outline-none focus:border-[#58a6ff]"
            >
              <option value={25}>25</option>
              <option value={50}>50</option>
              <option value={100}>100</option>
            </select>
          </div>
          <div className="flex items-center space-x-3">
            <button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - limit))}
              className="p-1.5 rounded-md bg-[#161b22] hover:bg-[#21262d] disabled:opacity-30 border border-[#30363d] text-[#c9d1d9] transition-colors"
            >
              <ChevronLeft className="w-4 h-4" />
            </button>
            <span>Offset: <span className="text-[#c9d1d9] font-semibold">{offset}</span></span>
            <button
              disabled={offset + limit >= total}
              onClick={() => setOffset(offset + limit)}
              className="p-1.5 rounded-md bg-[#161b22] hover:bg-[#21262d] disabled:opacity-30 border border-[#30363d] text-[#c9d1d9] transition-colors"
            >
              <ChevronRight className="w-4 h-4" />
            </button>
          </div>
        </div>
      </div>

      {/* Detail Modal */}
      {selectedPreview && (
        <div
          role="dialog"
          aria-modal="true"
          aria-labelledby="quarantine-detail-title"
          className="!m-0 fixed inset-0 bg-black/80 backdrop-blur-sm flex items-center justify-center p-4 z-50 overflow-y-auto animate-modal-backdrop"
        >
          <div className="bg-[#161b22] border border-[#30363d] rounded-lg max-w-2xl w-full p-5 sm:p-6 space-y-4 max-h-[90vh] flex flex-col min-w-0 overflow-y-auto animate-modal-content shadow-2xl">
            <div className="flex items-center justify-between pb-3 border-b border-[#21262d] gap-2 min-w-0">
              <h3 id="quarantine-detail-title" className="text-sm font-mono font-bold text-rose-400 truncate">
                Quarantine Detail: {selectedPreview.quarantine_id}
              </h3>
              <button
                onClick={() => setSelectedPreview(null)}
                aria-label="Close quarantine details"
                className="text-xs font-mono text-[#8b949e] hover:text-white bg-[#21262d] px-2.5 py-1 rounded-md shrink-0 flex items-center hover:bg-[#30363d] transition-colors border border-[#30363d]"
              >
                <X className="w-3.5 h-3.5 mr-1" />
                Close
              </button>
            </div>

            <div className="text-xs font-mono space-y-2.5 bg-[#0d1117] p-3.5 rounded-md border border-[#21262d] break-all">
              <div><span className="text-[#8b949e]">Raw ID:</span> <span className="text-[#c9d1d9] font-medium">{selectedPreview.raw_id}</span></div>
              <div><span className="text-[#8b949e]">Raw SHA-256:</span> <span className="text-emerald-400 font-semibold">{selectedPreview.raw_sha256}</span></div>
              <div><span className="text-[#8b949e]">Stage:</span> <span className="text-amber-400 font-medium">{selectedPreview.stage}</span></div>
              <div><span className="text-[#8b949e]">Reason:</span> <span className="text-[#f0f6fc] font-medium">{selectedPreview.reason}</span></div>
              <div><span className="text-[#8b949e]">Error Details:</span> <span className="text-rose-400 font-semibold">{selectedPreview.error_details}</span></div>
            </div>

            <div className="min-w-0">
              <div className="text-xs font-mono text-[#8b949e] mb-1.5 font-semibold">Payload Preview:</div>
              <pre className="bg-[#0d1117] border border-[#21262d] p-3.5 rounded-md text-xs font-mono text-[#c9d1d9] overflow-auto max-h-48 whitespace-pre-wrap w-full max-w-full min-w-0 leading-relaxed">
                {selectedPreview.raw_preview}
              </pre>
            </div>

            <div className="flex justify-end space-x-3 pt-2">
              {/* <button
                onClick={() => setSelectedPreview(null)}
                className="px-3.5 py-1.5 bg-[#21262d] hover:bg-[#30363d] text-xs font-mono font-medium rounded-md text-[#c9d1d9] hover:text-white transition-colors border border-[#30363d]"
              >
                Close
              </button> */}
              <button
                onClick={() => {
                  const id = selectedPreview.quarantine_id;
                  setSelectedPreview(null);
                  handleReplay(id);
                }}
                className="px-4 py-1.5 bg-[#238636] hover:bg-[#2ea043] text-white text-xs font-mono font-medium rounded-md flex items-center shrink-0 transition-colors shadow-xs"
              >
                <Play className="w-3.5 h-3.5 mr-1.5" />
                Replay Event
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
