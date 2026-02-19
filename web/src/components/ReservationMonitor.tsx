import { useState, useEffect } from 'react';
import type { GPUReservation } from '../types';

interface Props {
  reservations: GPUReservation[];
}

export default function ReservationMonitor({ reservations }: Props) {
  const active = reservations.filter((r) => r.Status === 'active');
  const [, setTick] = useState(0);

  // Re-render every second for live TTL countdown.
  useEffect(() => {
    if (active.length === 0) return;
    const id = setInterval(() => setTick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, [active.length]);

  if (active.length === 0) {
    return (
      <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
        <h3 className="text-lg font-semibold mb-2">Reservation Monitor</h3>
        <p className="text-sm text-gray-500">No active reservations</p>
      </div>
    );
  }

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <h3 className="text-lg font-semibold mb-3">Reservation Monitor</h3>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-gray-400 text-xs uppercase border-b border-gray-700">
            <th className="text-left py-2">ID</th>
            <th className="text-left py-2">For App</th>
            <th className="text-left py-2">Cluster</th>
            <th className="text-left py-2">Node</th>
            <th className="text-center py-2">GPUs</th>
            <th className="text-right py-2">TTL</th>
          </tr>
        </thead>
        <tbody>
          {active.map((r) => {
            const remainingMs = new Date(r.CreatedAt).getTime() + r.TTLSeconds * 1000 - Date.now();
            const remainingSec = Math.max(0, Math.ceil(remainingMs / 1000));
            const color =
              remainingMs <= 0
                ? 'text-red-400 animate-pulse'
                : remainingSec < 30
                  ? 'text-red-400 animate-pulse'
                  : remainingSec < 60
                    ? 'text-yellow-400'
                    : 'text-green-400';

            return (
              <tr key={r.ReservationID} className="border-b border-gray-700/50">
                <td className="py-2 font-mono text-xs">{r.ReservationID.slice(0, 8)}</td>
                <td className="py-2">{r.ForAppID}</td>
                <td className="py-2">{r.ClusterID}</td>
                <td className="py-2 text-xs">{r.NodeID || '-'}</td>
                <td className="py-2 text-center">{r.GPUs}</td>
                <td className={`py-2 text-right font-mono ${color}`}>
                  {remainingMs <= 0 ? 'Expiring...' : `${remainingSec}s`}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
