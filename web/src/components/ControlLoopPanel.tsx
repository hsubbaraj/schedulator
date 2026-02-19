import { useMemo } from 'react';
import type { EventRecord, CycleSummary } from '../types';

interface Props {
  cycleHistory: EventRecord[];
}

export default function ControlLoopPanel({ cycleHistory }: Props) {
  const data = useMemo(() => {
    return cycleHistory
      .map((ev) => {
        try {
          const summary = JSON.parse(ev.detail_json) as CycleSummary;
          return {
            timestamp: new Date(ev.timestamp),
            duration: summary.cycle_duration_ms,
            ops: summary.placement.scale_up_count + summary.placement.scale_down_count + summary.placement.preemption_count,
            triggers: summary.trigger_kinds || [],
          };
        } catch {
          return null;
        }
      })
      .filter((d): d is NonNullable<typeof d> => d !== null)
      .reverse();
  }, [cycleHistory]);

  const latest = data[data.length - 1];
  const avgDuration = data.length > 0 ? data.reduce((acc, d) => acc + d.duration, 0) / data.length : 0;
  const maxDuration = data.length > 0 ? Math.max(...data.map(d => d.duration)) : 0;

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
      <div className="px-4 py-3 border-b border-gray-700 bg-gray-800/50 flex justify-between items-center">
        <h3 className="font-semibold text-gray-200">Control Loop Heartbeat</h3>
        <div className="flex items-center gap-1.5">
          <span className={`w-2 h-2 rounded-full bg-green-500 animate-pulse`} />
          <span className="text-xs text-green-500 font-medium">Live</span>
        </div>
      </div>

      <div className="p-4 grid grid-cols-1 md:grid-cols-4 gap-4">
        <div className="bg-gray-900/50 p-3 rounded-lg border border-gray-700">
          <div className="text-[10px] text-gray-500 uppercase font-bold mb-1">Avg Cycle</div>
          <div className="text-xl font-mono text-blue-400">{Math.round(avgDuration)}ms</div>
        </div>
        <div className="bg-gray-900/50 p-3 rounded-lg border border-gray-700">
          <div className="text-[10px] text-gray-500 uppercase font-bold mb-1">Max Cycle</div>
          <div className="text-xl font-mono text-red-400">{Math.round(maxDuration)}ms</div>
        </div>
        <div className="bg-gray-900/50 p-3 rounded-lg border border-gray-700">
          <div className="text-[10px] text-gray-500 uppercase font-bold mb-1">Last Triggers</div>
          <div className="text-xs font-medium text-gray-300 truncate">
            {latest?.triggers.join(', ') || 'None'}
          </div>
        </div>
        <div className="bg-gray-900/50 p-3 rounded-lg border border-gray-700">
          <div className="text-[10px] text-gray-500 uppercase font-bold mb-1">Cycles Tracked</div>
          <div className="text-xl font-mono text-gray-300">{data.length}</div>
        </div>
      </div>

      <div className="px-4 pb-4">
        <div className="bg-gray-950 rounded border border-gray-800 h-24 flex items-end p-1 gap-px">
          {data.slice(-100).map((d, i) => (
            <div
              key={i}
              className={`flex-1 min-w-[2px] transition-all hover:bg-white/20 rounded-t-sm ${
                d.duration > 500 ? 'bg-red-500' : d.duration > 200 ? 'bg-yellow-500' : 'bg-blue-500'
              }`}
              style={{ height: `${Math.min(100, (d.duration / Math.max(1000, maxDuration)) * 100)}%` }}
              title={`Cycle at ${d.timestamp.toLocaleTimeString()}: ${d.duration}ms, ${d.ops} ops`}
            />
          ))}
        </div>
        <div className="flex justify-between mt-1 px-1">
           <span className="text-[10px] text-gray-600">{data[0]?.timestamp.toLocaleTimeString()}</span>
           <span className="text-[10px] text-gray-500">Cycle Duration History</span>
           <span className="text-[10px] text-gray-600">{latest?.timestamp.toLocaleTimeString()}</span>
        </div>
      </div>
    </div>
  );
}
