import { useMemo, useState } from 'react';
import type { EventRecord, CycleSummary } from '../types';
import ControlLoopDetailModal from './ControlLoopDetailModal';

interface Props {
  cycleHistory: EventRecord[];
}

export default function ControlLoopTable({ cycleHistory }: Props) {
  const [selectedEventID, setSelectedEventID] = useState<number | null>(null);

  const data = useMemo(() => {
    return cycleHistory
      .map((ev) => {
        try {
          const summary = JSON.parse(ev.detail_json) as CycleSummary;
          return {
            event: ev,
            id: ev.id,
            timestamp: new Date(ev.timestamp),
            duration: summary.cycle_duration_ms,
            ops: summary.placement.scale_up_count + summary.placement.scale_down_count + summary.placement.preemption_count,
            triggers: summary.trigger_kinds || [],
          };
        } catch {
          return null;
        }
      })
      .filter((d): d is NonNullable<typeof d> => d !== null);
  }, [cycleHistory]);

  const selectedEvent = data.find(d => d.id === selectedEventID)?.event;

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
      <div className="px-4 py-3 border-b border-gray-700 bg-gray-800/50 flex justify-between items-center">
        <h3 className="font-semibold text-gray-200">Control Loop Cycles</h3>
        <div className="flex items-center gap-1.5">
          <span className={`w-2 h-2 rounded-full bg-green-500 animate-pulse`} />
          <span className="text-xs text-green-500 font-medium">Live</span>
        </div>
      </div>

      <div className="overflow-x-auto max-h-[400px]">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-gray-800 shadow-sm">
            <tr className="border-b border-gray-700 text-gray-400 text-xs uppercase tracking-wider">
              <th className="text-left p-3">ID</th>
              <th className="text-left p-3">Time</th>
              <th className="text-center p-3">Cycle Time</th>
              <th className="text-center p-3">Ops</th>
              <th className="text-left p-3">Trigger</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-700/50">
            {data.map((d) => (
              <tr
                key={d.id}
                className="hover:bg-gray-750 cursor-pointer transition-colors group"
                onClick={() => setSelectedEventID(d.id)}
              >
                <td className="p-3 font-mono text-gray-500">#{d.id}</td>
                <td className="p-3 text-gray-300">
                  {d.timestamp.toLocaleTimeString()}
                </td>
                <td className="p-3 text-center">
                  <span className={`font-mono ${
                    d.duration > 500 ? 'text-red-400' : d.duration > 200 ? 'text-yellow-400' : 'text-blue-400'
                  }`}>
                    {d.duration}ms
                  </span>
                </td>
                <td className="p-3 text-center text-gray-400">
                  {d.ops > 0 ? (
                    <span className="text-indigo-400 font-bold">{d.ops}</span>
                  ) : '-'}
                </td>
                <td className="p-3">
                  <div className="flex flex-wrap gap-1">
                    {d.triggers.map((t) => (
                      <span key={t} className="text-[10px] uppercase font-bold px-1 py-0.5 rounded bg-gray-900 text-gray-500 border border-gray-700 group-hover:border-gray-600">
                        {t}
                      </span>
                    ))}
                  </div>
                </td>
              </tr>
            ))}
            {data.length === 0 && (
              <tr>
                <td colSpan={5} className="p-8 text-center text-gray-500 italic">
                  No control loop cycles recorded yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {selectedEvent && (
        <ControlLoopDetailModal
          event={selectedEvent}
          onClose={() => setSelectedEventID(null)}
        />
      )}
    </div>
  );
}
