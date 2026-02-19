import { useState, useEffect, useCallback } from 'react';
import { LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer, ReferenceLine } from 'recharts';
import type { ClusterSnapshot, GPUReservation } from '../types';

interface Props {
  clusterID: string;
  reservations?: GPUReservation[];
}

export default function GpuTimeline({ clusterID, reservations = [] }: Props) {
  const [data, setData] = useState<ClusterSnapshot[]>([]);

  const fetchSnapshots = useCallback(async () => {
    try {
      const since = new Date(Date.now() - 60 * 60 * 1000).toISOString();
      const res = await fetch(`/api/v1/snapshots?cluster_id=${clusterID}&since=${since}`);
      if (res.ok) {
        const snaps: ClusterSnapshot[] = await res.json();
        setData(snaps || []);
      }
    } catch {
      // Silent fail.
    }
  }, [clusterID]);

  useEffect(() => {
    fetchSnapshots();
    const interval = setInterval(fetchSnapshots, 30000);
    return () => clearInterval(interval);
  }, [fetchSnapshots]);

  // Current reserved GPUs for this cluster
  const reservedGPUs = reservations
    .filter((r) => r.ClusterID === clusterID && r.Status === 'active')
    .reduce((sum, r) => sum + r.GPUs, 0);

  if (data.length === 0) {
    return (
      <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
        <h4 className="text-sm font-semibold mb-2">GPU Timeline: {clusterID}</h4>
        <p className="text-xs text-gray-500">No snapshot data yet</p>
      </div>
    );
  }

  const chartData = data.map((snap) => ({
    time: new Date(snap.timestamp).toLocaleTimeString(),
    total: snap.total_gpus,
    allocated: snap.allocated_gpus,
    free: snap.free_gpus,
  }));

  // Compute the current allocated level for the reservation line
  const lastSnap = data[data.length - 1];
  const reservationLine = lastSnap ? lastSnap.allocated_gpus + reservedGPUs : 0;

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <div className="flex items-center justify-between mb-2">
        <h4 className="text-sm font-semibold">GPU Timeline: {clusterID}</h4>
        {reservedGPUs > 0 && (
          <span className="text-xs px-2 py-0.5 rounded-full bg-yellow-900/50 text-yellow-400 border border-yellow-700">
            {reservedGPUs} GPUs reserved
          </span>
        )}
      </div>
      <ResponsiveContainer width="100%" height={200}>
        <LineChart data={chartData}>
          <CartesianGrid strokeDasharray="3 3" stroke="#374151" />
          <XAxis dataKey="time" tick={{ fontSize: 10, fill: '#9CA3AF' }} />
          <YAxis tick={{ fontSize: 10, fill: '#9CA3AF' }} />
          <Tooltip
            contentStyle={{ backgroundColor: '#1F2937', border: '1px solid #374151', borderRadius: '8px' }}
            labelStyle={{ color: '#9CA3AF' }}
          />
          <Legend wrapperStyle={{ fontSize: '12px' }} />
          <Line type="monotone" dataKey="total" stroke="#6B7280" strokeDasharray="5 5" dot={false} />
          <Line type="monotone" dataKey="allocated" stroke="#3B82F6" dot={false} />
          <Line type="monotone" dataKey="free" stroke="#10B981" dot={false} />
          {reservedGPUs > 0 && (
            <ReferenceLine
              y={reservationLine}
              stroke="#EAB308"
              strokeDasharray="4 4"
              label={{ value: `+${reservedGPUs} reserved`, fill: '#EAB308', fontSize: 10, position: 'right' }}
            />
          )}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
