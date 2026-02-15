import type { WorldState, CycleSummary } from '../types';

interface Props {
  state: WorldState;
  latestCycle: CycleSummary | null;
}

export default function HeroStrip({ state, latestCycle }: Props) {
  // Tile 1: SLA Compliance
  const apps = Object.values(state.Applications);
  const metrics = state.VLLMMetrics || {};
  let slaTotal = 0;
  let slaMet = 0;
  for (const app of apps) {
    if (app.SLA && app.SLA.MaxP99TTFTMs > 0) {
      slaTotal++;
      const m = metrics[app.AppID];
      if (m && m.P99TimeToFirstTokenMs <= app.SLA.MaxP99TTFTMs) {
        slaMet++;
      }
    }
  }
  const slaColor =
    slaTotal === 0
      ? 'text-gray-400'
      : slaMet === slaTotal
        ? 'text-green-400'
        : slaMet >= slaTotal * 0.8
          ? 'text-yellow-400'
          : 'text-red-400';

  // Tile 2: Fleet GPU Utilization
  const clusters = Object.values(state.Clusters);
  let totalGPUs = 0;
  let allocatedGPUs = 0;
  for (const cluster of clusters) {
    if (!cluster.Nodes) continue;
    for (const node of Object.values(cluster.Nodes)) {
      if (node.Status !== 'ready') continue;
      totalGPUs += node.TotalGPUs;
      allocatedGPUs += node.AllocatedGPUs;
    }
  }
  const gpuPct = totalGPUs > 0 ? Math.round((allocatedGPUs / totalGPUs) * 100) : 0;
  const gpuColor = gpuPct > 90 ? 'text-red-400' : gpuPct > 70 ? 'text-yellow-400' : 'text-green-400';

  // Tile 3: Active Reservations
  const reservations = state.Reservations ? Object.values(state.Reservations) : [];
  const activeReservations = reservations.filter((r) => r.Status === 'active');
  const now = Date.now();
  const hasUrgentReservation = activeReservations.some((r) => {
    const remaining = new Date(r.CreatedAt).getTime() + r.TTLSeconds * 1000 - now;
    return remaining < 30000;
  });
  const reservationColor =
    activeReservations.length === 0
      ? 'text-gray-400'
      : hasUrgentReservation
        ? 'text-red-400'
        : 'text-yellow-400';

  // Tile 4: Control Loop Health
  let loopLabel = 'Waiting for first cycle...';
  let loopColor = 'text-gray-400';
  if (latestCycle) {
    loopLabel = `${latestCycle.cycle_duration_ms}ms`;
    loopColor = latestCycle.cycle_duration_ms < 500 ? 'text-green-400' : 'text-yellow-400';
  }

  return (
    <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
      <Tile label="SLA Compliance" value={slaTotal === 0 ? 'No SLAs' : `${slaMet}/${slaTotal}`} color={slaColor} />
      <Tile label="Fleet GPU Util" value={`${gpuPct}%`} sub={`${allocatedGPUs}/${totalGPUs} GPUs`} color={gpuColor} />
      <Tile label="Active Reservations" value={`${activeReservations.length}`} color={reservationColor} />
      <Tile label="Control Loop" value={loopLabel} color={loopColor} />
    </div>
  );
}

function Tile({ label, value, sub, color }: { label: string; value: string; sub?: string; color: string }) {
  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 p-4">
      <p className="text-xs text-gray-400 uppercase tracking-wider">{label}</p>
      <p className={`text-2xl font-bold mt-1 ${color}`}>{value}</p>
      {sub && <p className="text-xs text-gray-500 mt-0.5">{sub}</p>}
    </div>
  );
}
