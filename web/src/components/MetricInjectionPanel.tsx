import { useState } from 'react';
import type { WorldState } from '../types';

interface Props {
  state: WorldState;
}

export default function MetricInjectionPanel({ state }: Props) {
  const apps = Object.keys(state.Applications).sort();
  const [selectedApp, setSelectedApp] = useState<string>(apps[0] || '');
  const [queueDepth, setQueueDepth] = useState<string>('0');
  const [kvCache, setKVCache] = useState<string>('0');
  const [sending, setSending] = useState(false);
  const [status, setStatus] = useState<{ type: 'success' | 'error', msg: string } | null>(null);

  const handleInject = async () => {
    if (!selectedApp) return;
    setSending(true);
    setStatus(null);

    try {
      const resp = await fetch('/api/v1/inject/metrics', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          AppID: selectedApp,
          AvgWaitingQueueDepth: parseFloat(queueDepth) || 0,
          MaxKVCacheUtilization: parseFloat(kvCache) || 0,
          MeasuredAt: new Date().toISOString(),
        }),
      });

      if (resp.ok) {
        setStatus({ type: 'success', msg: 'Metrics injected!' });
      } else {
        const text = await resp.text();
        setStatus({ type: 'error', msg: `Failed: ${text}` });
      }
    } catch (err) {
      setStatus({ type: 'error', msg: `Error: ${err}` });
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <h3 className="text-lg font-semibold mb-3">Manually Inject Metrics</h3>
      
      <div className="space-y-4">
        <div>
          <label className="block text-xs font-medium text-gray-400 mb-1">Application</label>
          <select
            className="bg-gray-700 border border-gray-600 rounded px-3 py-1.5 text-sm w-full"
            value={selectedApp}
            onChange={(e) => setSelectedApp(e.target.value)}
          >
            {apps.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </select>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="block text-xs font-medium text-gray-400 mb-1">Avg Waiting Queue</label>
            <input
              type="number"
              step="0.1"
              min="0"
              className="bg-gray-700 border border-gray-600 rounded px-3 py-1.5 text-sm w-full"
              value={queueDepth}
              onChange={(e) => setQueueDepth(e.target.value)}
            />
          </div>
          <div>
            <label className="block text-xs font-medium text-gray-400 mb-1">Max KV Cache Util</label>
            <input
              type="number"
              step="0.01"
              min="0"
              max="1"
              className="bg-gray-700 border border-gray-600 rounded px-3 py-1.5 text-sm w-full"
              value={kvCache}
              onChange={(e) => setKVCache(e.target.value)}
            />
          </div>
        </div>

        <button
          className={`w-full py-2 rounded font-medium text-sm transition-colors ${
            sending ? 'bg-gray-600 cursor-not-allowed' : 'bg-blue-600 hover:bg-blue-500'
          }`}
          onClick={handleInject}
          disabled={sending}
        >
          {sending ? 'Injecting...' : 'Inject Metrics Event'}
        </button>

        {status && (
          <div className={`text-xs p-2 rounded ${status.type === 'success' ? 'bg-green-900/30 text-green-400' : 'bg-red-900/30 text-red-400'}`}>
            {status.msg}
          </div>
        )}
      </div>
    </div>
  );
}
