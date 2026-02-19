import { useState } from 'react';
import type { EventRecord, CycleSummary } from '../types';

interface Props {
  events: EventRecord[];
}

const typeColors: Record<string, string> = {
  scale_up: 'text-green-400',
  scale_down: 'text-orange-400',
  preempt: 'text-red-400',
  migrate: 'text-blue-400',
  node_down: 'text-red-500',
  node_up: 'text-green-500',
  app_added: 'text-purple-400',
  cycle_complete: 'text-blue-400',
  cycle_summary: 'text-blue-400',
  state_update: 'text-gray-500',
};

const typeBgColors: Record<string, string> = {
  scale_up: 'border-l-2 border-green-500/50',
  scale_down: 'border-l-2 border-orange-500/50',
  preempt: 'border-l-2 border-red-500/50',
  cycle_complete: 'border-l-2 border-blue-500/50',
  cycle_summary: 'border-l-2 border-blue-500/50',
};

export default function EventStream({ events }: Props) {
  const [expandedId, setExpandedId] = useState<number | null>(null);

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <h3 className="text-lg font-semibold mb-3">Event Stream</h3>
      <div className="space-y-1 max-h-96 overflow-y-auto">
        {events.length === 0 ? (
          <p className="text-sm text-gray-500">No events yet</p>
        ) : (
          events.map((event) => {
            const isCycleSummary = event.type === 'cycle_summary';
            return (
              <div key={event.id} className={`text-sm ${typeBgColors[event.type] || ''}`}>
                <button
                  className="w-full text-left flex items-start gap-2 py-1 hover:bg-gray-750 rounded px-1"
                  onClick={() => setExpandedId(expandedId === event.id ? null : event.id)}
                >
                  <span className="text-xs text-gray-500 w-20 shrink-0 font-mono">
                    {new Date(event.timestamp).toLocaleTimeString()}
                  </span>
                  <span className={`w-24 shrink-0 font-medium ${typeColors[event.type] || 'text-gray-300'}`}>
                    {event.type}
                  </span>
                  <span className="text-gray-300 truncate">{event.summary}</span>
                </button>
                {expandedId === event.id && event.detail_json && (
                  <div className="ml-8 mt-1 mb-1">
                    {isCycleSummary ? (
                      <CycleSummaryCard json={event.detail_json} />
                    ) : (
                      <pre className="text-xs text-gray-400 bg-gray-900 rounded p-2 overflow-x-auto">
                        {(() => {
                          try {
                            return JSON.stringify(JSON.parse(event.detail_json), null, 2);
                          } catch {
                            return event.detail_json;
                          }
                        })()}
                      </pre>
                    )}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

function CycleSummaryCard({ json }: { json: string }) {
  let cs: CycleSummary;
  try {
    cs = JSON.parse(json);
  } catch {
    return <pre className="text-xs text-gray-400 bg-gray-900 rounded p-2">{json}</pre>;
  }

  const decisions = cs.scaling_decisions ? Object.entries(cs.scaling_decisions) : [];
  const activeDecisions = decisions.filter(([, d]) => d.Direction !== 'unchanged');

  return (
    <div className="text-xs bg-gray-900 rounded p-2 space-y-1">
      <div className="flex gap-3 text-gray-300">
        <span className="text-green-400">+{cs.placement.scale_up_count} up</span>
        <span className="text-orange-400">-{cs.placement.scale_down_count} down</span>
        <span className="text-red-400">{cs.placement.preemption_count} preempt</span>
        <span className="text-gray-500">{cs.cycle_duration_ms}ms</span>
      </div>
      {activeDecisions.length > 0 && (
        <div className="mt-1 space-y-0.5">
          {activeDecisions.map(([appID, d]) => (
            <div key={appID} className="text-gray-400">
              <span className="font-medium text-gray-300">{appID}</span>: {d.Direction} ({d.Signal})
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
