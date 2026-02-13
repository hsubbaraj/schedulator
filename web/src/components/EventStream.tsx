import { useState } from 'react';
import type { EventRecord } from '../types';

interface Props {
  events: EventRecord[];
}

const typeColors: Record<string, string> = {
  scale_up: 'text-green-400',
  scale_down: 'text-yellow-400',
  preempt: 'text-red-400',
  migrate: 'text-blue-400',
  node_down: 'text-red-500',
  node_up: 'text-green-500',
  app_added: 'text-purple-400',
  cycle_complete: 'text-gray-400',
  state_update: 'text-gray-500',
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
          events.map((event) => (
            <div key={event.id} className="text-sm">
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
                <pre className="text-xs text-gray-400 bg-gray-900 rounded p-2 ml-8 mt-1 overflow-x-auto">
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
          ))
        )}
      </div>
    </div>
  );
}
