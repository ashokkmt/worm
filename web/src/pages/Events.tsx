import React, { useEffect, useState } from 'react';
import { Search, RefreshCw, Eye, ShieldCheck, ChevronLeft, ChevronRight, X } from 'lucide-react';
import { NormalizedEvent, EventTrace } from '../types/event';
import { apiService } from '../services/api';
import { Badge } from '../components/Badge';
import { TraceViewer } from '../components/TraceViewer';

interface EventsProps {
  initialEventId?: string;
}

export const Events: React.FC<EventsProps> = ({ initialEventId }) => {
  const [events, setEvents] = useState<NormalizedEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [limit, setLimit] = useState(25);
  const [offset, setOffset] = useState(0);
  const [category, setCategory] = useState('all');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(false);

  // Selected event for Trace / Detail drawer
  const [selectedEvent, setSelectedEvent] = useState<NormalizedEvent | null>(null);
  const [traceData, setTraceData] = useState<EventTrace | null>(null);
  const [loadingTrace, setLoadingTrace] = useState(false);

  const fetchEvents = async () => {
    setLoading(true);
    try {
      const data = await apiService.listEvents({
        category: category !== 'all' ? category : undefined,
        q: search ? search : undefined,
        limit,
        offset,
      });
      setEvents(data.events);
      setTotal(data.total);
    } catch (e) {
      console.error('Failed to query events', e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchEvents();
  }, [category, limit, offset]);

  // Handle initialEventId if passed from navigation
  useEffect(() => {
    if (initialEventId) {
      handleInspectTrace(initialEventId);
    }
  }, [initialEventId]);

  const handleInspectTrace = async (eventId: string) => {
    setLoadingTrace(true);
    try {
      const trace = await apiService.getEventTrace(eventId);
      setTraceData(trace);
    } catch (e: any) {
      alert('Failed to load trace: ' + e.message);
    } finally {
      setLoadingTrace(false);
    }
  };

  const renderSeverityBadge = (sev: any) => {
    const s = Number(sev);
    switch (s) {
      case 5:
      case 6:
        return <Badge variant="red">CRITICAL ({s})</Badge>;
      case 4:
        return <Badge variant="amber">HIGH ({s})</Badge>;
      case 3:
        return <Badge variant="blue">MEDIUM ({s})</Badge>;
      default:
        return <Badge variant="gray">INFO ({s || 1})</Badge>;
    }
  };

  return (
    <div className="space-y-4">
      {/* Search and Filters Bar */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md p-4">
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-3 w-full md:w-auto">
            {/* Search Input */}
            <div className="relative w-full sm:w-64">
              <Search className="w-3.5 h-3.5 absolute left-3 top-2.5 text-[#8b949e]" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && fetchEvents()}
                placeholder="Search message, event_id, source..."
                className="bg-[#0d1117] border border-[#30363d] rounded text-xs font-mono text-[#c9d1d9] pl-8 pr-3 py-1.5 focus:outline-none focus:border-[#58a6ff] w-full"
              />
            </div>

            {/* Category Filter */}
            <select
              value={category}
              onChange={(e) => {
                setCategory(e.target.value);
                setOffset(0);
              }}
              className="bg-[#0d1117] border border-[#30363d] rounded text-xs font-mono text-[#c9d1d9] px-2.5 py-1.5 focus:outline-none focus:border-[#58a6ff] w-full sm:w-auto"
            >
              <option value="all">All Categories</option>
              <option value="network_device">Network Device</option>
              <option value="server">Server</option>
              <option value="os">Operating System</option>
              <option value="endpoint">Endpoint / IDS</option>
              <option value="application">Application</option>
              <option value="database">Database</option>
            </select>

            <button
              onClick={() => {
                setOffset(0);
                fetchEvents();
              }}
              className="px-3 py-1.5 bg-[#21262d] hover:bg-[#30363d] text-xs font-mono text-[#c9d1d9] rounded border border-[#30363d] flex items-center justify-center w-full sm:w-auto"
            >
              <RefreshCw className={`w-3 h-3 mr-1.5 ${loading ? 'animate-spin' : ''}`} />
              Query
            </button>
          </div>

          <div className="flex items-center space-x-2 text-xs font-mono text-[#8b949e]">
            <span>Showing {events.length} of {total.toLocaleString()}</span>
          </div>
        </div>
      </div>

      {/* Main Events Table */}
      <div className="bg-[#161b22] border border-[#30363d] rounded-md overflow-hidden">
        <div className="overflow-x-auto max-w-full">
          <table className="w-full text-left text-xs font-mono">
            <thead>
              <tr className="bg-[#0d1117] text-[#8b949e] border-b border-[#21262d]">
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Event ID</th>
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Timestamp</th>
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Category</th>
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Source ID</th>
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Activity</th>
                <th className="py-2.5 px-3 font-medium whitespace-nowrap">Severity</th>
                <th className="py-2.5 px-3 font-medium text-right whitespace-nowrap">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#21262d]">
              {events.length === 0 ? (
                <tr>
                  <td colSpan={7} className="py-10 text-center text-[#8b949e]">
                    {loading ? 'Executing search...' : 'No normalized events matched current filter criteria.'}
                  </td>
                </tr>
              ) : (
                events.map((evt) => (
                  <tr key={evt.worm.event_id} className="hover:bg-[#21262d]/50 transition-colors">
                    <td className="py-2 px-3 font-semibold text-[#58a6ff] whitespace-nowrap">
                      {evt.worm.event_id}
                    </td>
                    <td className="py-2 px-3 text-[#8b949e] whitespace-nowrap">
                      {evt.worm.received_time ? new Date(evt.worm.received_time).toLocaleTimeString() : 'N/A'}
                    </td>
                    <td className="py-2 px-3 whitespace-nowrap">
                      <Badge variant="blue">{evt.worm.source_category}</Badge>
                    </td>
                    <td className="py-2 px-3 text-[#f0f6fc] whitespace-nowrap">
                      {evt.worm.source_id}
                    </td>
                    <td className="py-2 px-3 text-[#c9d1d9] whitespace-nowrap">
                      {evt.ocsf?.activity_name || evt.ocsf?.type_name || 'Event'}
                    </td>
                    <td className="py-2 px-3 whitespace-nowrap">
                      {renderSeverityBadge(evt.ocsf?.severity_id)}
                    </td>
                    <td className="py-2 px-3 text-right space-x-2 whitespace-nowrap">
                      <button
                        onClick={() => handleInspectTrace(evt.worm.event_id)}
                        className="px-2 py-0.5 rounded bg-[#21262d] hover:bg-[#30363d] text-emerald-400 border border-emerald-900/60 inline-flex items-center text-[11px]"
                        title="Forensic Trace & SHA-256 Verification"
                      >
                        <ShieldCheck className="w-3 h-3 mr-1" />
                        Trace
                      </button>
                      <button
                        onClick={() => setSelectedEvent(evt)}
                        className="px-2 py-0.5 rounded bg-[#21262d] hover:bg-[#30363d] text-[#c9d1d9] border border-[#30363d] inline-flex items-center text-[11px]"
                        title="View JSON Document"
                      >
                        <Eye className="w-3 h-3 mr-1" />
                        JSON
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>

        {/* Pagination Bar */}
        <div className="bg-[#0d1117] px-4 py-2.5 border-t border-[#21262d] flex flex-col sm:flex-row items-center justify-between text-xs font-mono text-[#8b949e] gap-2">
          <div className="flex items-center space-x-2">
            <span>Page size:</span>
            <select
              value={limit}
              onChange={(e) => {
                setLimit(Number(e.target.value));
                setOffset(0);
              }}
              className="bg-[#161b22] border border-[#30363d] rounded text-xs px-2 py-1 text-[#c9d1d9]"
            >
              <option value={25}>25</option>
              <option value={50}>50</option>
              <option value={100}>100</option>
            </select>
          </div>

          <div className="flex items-center space-x-2">
            <button
              disabled={offset === 0}
              onClick={() => setOffset(Math.max(0, offset - limit))}
              className="p-1 rounded bg-[#161b22] disabled:opacity-30 border border-[#30363d] text-[#c9d1d9]"
            >
              <ChevronLeft className="w-4 h-4" />
            </button>
            <span>
              Offset: {offset} - {Math.min(total, offset + limit)} of {total}
            </span>
            <button
              disabled={offset + limit >= total}
              onClick={() => setOffset(offset + limit)}
              className="p-1 rounded bg-[#161b22] disabled:opacity-30 border border-[#30363d] text-[#c9d1d9]"
            >
              <ChevronRight className="w-4 h-4" />
            </button>
          </div>
        </div>
      </div>

      {/* Forensic Trace Viewer Modal */}
      {(traceData || loadingTrace) && (
        <div className="fixed inset-0 bg-black/70 backdrop-blur-xs flex items-center justify-center p-4 z-50 overflow-y-auto animate-modal-backdrop">
          <div className="max-w-4xl w-full my-auto max-h-[90vh] overflow-y-auto rounded-md min-w-0 shadow-2xl animate-modal-content">
            {loadingTrace ? (
              <div className="bg-[#161b22] border border-[#30363d] rounded-md p-8 text-center text-xs font-mono text-[#8b949e]">
                Loading forensic lineage trace...
              </div>
            ) : traceData ? (
              <TraceViewer trace={traceData} onClose={() => setTraceData(null)} />
            ) : null}
          </div>
        </div>
      )}

      {/* JSON Inspection Modal */}
      {selectedEvent && (
        <div className="fixed inset-0 bg-black/70 backdrop-blur-xs flex items-center justify-center p-4 z-50 overflow-y-auto animate-modal-backdrop">
          <div className="bg-[#161b22] border border-[#30363d] rounded-md max-w-3xl w-full p-5 space-y-4 my-auto max-h-[90vh] flex flex-col min-w-0 animate-modal-content">
            <div className="flex items-center justify-between pb-3 border-b border-[#21262d] gap-2 min-w-0">
              <h3 className="text-sm font-mono font-bold text-[#f0f6fc] truncate">
                Event Record: {selectedEvent.worm.event_id}
              </h3>
              <button
                onClick={() => setSelectedEvent(null)}
                className="text-[#8b949e] hover:text-[#c9d1d9] p-1 rounded hover:bg-[#21262d] shrink-0"
                aria-label="Close JSON preview"
              >
                {/* <X className="w-4 h-4" /> */}
              </button>
            </div>
            <pre className="bg-[#0d1117] border border-[#21262d] p-3 rounded-md text-xs font-mono text-[#c9d1d9] overflow-auto max-h-[65vh] w-full max-w-full min-w-0">
              {JSON.stringify(selectedEvent, null, 2)}
            </pre>
            <div className="flex justify-end pt-2">
              <button
                onClick={() => setSelectedEvent(null)}
                className="px-3 py-1.5 bg-[#21262d] text-xs font-mono rounded text-[#c9d1d9] hover:bg-[#30363d]"
              >
                Close
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
