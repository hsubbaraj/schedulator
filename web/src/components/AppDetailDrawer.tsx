import { useState } from 'react';
import type { WorldState, CycleSummary, ScalingConfig, Replica, CacheLocation } from '../types';

interface Props {
  appID: string;
  state: WorldState;
  latestCycle: CycleSummary | null;
  scalingConfig: ScalingConfig | null;
  onClose: () => void;
}

type Tab = 'scaling' | 'metrics' | 'replicas' | 'failure' | 'cache';

export default function AppDetailDrawer({ appID, state, latestCycle, scalingConfig, onClose }: Props) {
  const [tab, setTab] = useState<Tab>('scaling');
  const app = state.Applications[appID];
  if (!app) return null;

  const tabs: { key: Tab; label: string }[] = [
    { key: 'scaling', label: 'Scaling' },
    { key: 'metrics', label: 'Metrics' },
    { key: 'replicas', label: 'Replicas' },
    { key: 'failure', label: 'Failure Domain' },
    { key: 'cache', label: 'Cache' },
  ];

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <div className="absolute inset-0 bg-black/50" onClick={onClose} />
      <div className="relative w-full max-w-lg bg-gray-900 border-l border-gray-700 overflow-y-auto">
        <div className="sticky top-0 bg-gray-900 border-b border-gray-700 p-4 flex items-center justify-between">
          <h2 className="text-lg font-bold">{appID}</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-white text-xl">
            &times;
          </button>
        </div>
        <div className="flex border-b border-gray-700">
          {tabs.map((t) => (
            <button
              key={t.key}
              className={`px-4 py-2 text-sm ${tab === t.key ? 'text-blue-400 border-b-2 border-blue-400' : 'text-gray-400 hover:text-gray-200'}`}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </div>
        <div className="p-4">
          {tab === 'scaling' && <ScalingTab appID={appID} state={state} latestCycle={latestCycle} scalingConfig={scalingConfig} />}
          {tab === 'metrics' && <MetricsTab appID={appID} state={state} scalingConfig={scalingConfig} />}
          {tab === 'replicas' && <ReplicasTab appID={appID} state={state} />}
          {tab === 'failure' && <FailureDomainTab appID={appID} app={state.Applications[appID]} state={state} />}
          {tab === 'cache' && <CacheTab appID={appID} state={state} />}
        </div>
      </div>
    </div>
  );
}

function ScalingTab({ appID, state, latestCycle, scalingConfig }: { appID: string; state: WorldState; latestCycle: CycleSummary | null; scalingConfig: ScalingConfig | null }) {
  const history = state.ScalingHistory?.[appID];
  const decision = latestCycle?.scaling_decisions?.[appID];
  const now = Date.now();

  const cooldownRemaining = (lastAt: string | undefined, cooldownSec: number | undefined) => {
    if (!lastAt || !cooldownSec) return 0;
    const t = new Date(lastAt).getTime();
    if (t <= 0) return 0;
    return Math.max(0, t + cooldownSec * 1000 - now);
  };

  const scaleUpCooldown = cooldownRemaining(history?.LastScaleUpAt, scalingConfig?.ScaleUpCooldownSeconds);
  const scaleDownCooldown = cooldownRemaining(history?.LastScaleDownAt, scalingConfig?.ScaleDownCooldownSeconds);

  return (
    <div className="space-y-3 text-sm">
      <Row label="Last scale-up" value={history?.LastScaleUpAt ? formatTime(history.LastScaleUpAt) : 'Never'} />
      <Row label="Last scale-down" value={history?.LastScaleDownAt ? formatTime(history.LastScaleDownAt) : 'Never'} />
      <Row label="Scale-up cooldown" value={scaleUpCooldown > 0 ? `${Math.ceil(scaleUpCooldown / 1000)}s remaining` : 'Ready'} color={scaleUpCooldown > 0 ? 'text-yellow-400' : 'text-green-400'} />
      <Row label="Scale-down cooldown" value={scaleDownCooldown > 0 ? `${Math.ceil(scaleDownCooldown / 1000)}s remaining` : 'Ready'} color={scaleDownCooldown > 0 ? 'text-yellow-400' : 'text-green-400'} />
      <Row label="Consecutive scale-down signals" value={`${history?.ConsecutiveScaleDownSignals || 0} / ${scalingConfig?.StabilizationCycles || '?'}`} />
      {decision && (
        <>
          <div className="border-t border-gray-700 pt-2 mt-2 text-xs text-gray-400 uppercase">Current Decision</div>
          <Row label="Direction" value={decision.Direction} color={decision.Direction === 'up' ? 'text-green-400' : decision.Direction === 'down' ? 'text-orange-400' : 'text-gray-400'} />
          <Row label="Signal" value={decision.Signal} />
          <Row label="Current / Target" value={`${decision.CurrentCount} / ${decision.TargetCount}`} />
          <Row label="Action approved" value={decision.ScaleActionApproved ? 'Yes' : 'No'} color={decision.ScaleActionApproved ? 'text-green-400' : 'text-gray-400'} />
          {decision.SLABreach && <Row label="SLA Breach" value="Yes" color="text-red-400" />}
        </>
      )}
    </div>
  );
}

function MetricsTab({ appID, state, scalingConfig }: { appID: string; state: WorldState; scalingConfig: ScalingConfig | null }) {
  const m = state.VLLMMetrics?.[appID];
  if (!m) return <p className="text-gray-500 text-sm">No metrics available</p>;

  const kvColor = (v: number) =>
    v >= (scalingConfig?.KVCacheHighWatermark || 0.85) ? 'text-red-400' :
    v >= (scalingConfig?.KVCacheLowWatermark || 0.40) ? 'text-yellow-400' :
    'text-green-400';

  const qColor = (v: number) =>
    v >= (scalingConfig?.QueueHighWatermark || 10) ? 'text-red-400' :
    v >= (scalingConfig?.QueueLowWatermark || 1) ? 'text-yellow-400' :
    'text-green-400';

  return (
    <div className="space-y-2 text-sm">
      <Row label="Avg Queue Depth" value={m.AvgWaitingQueueDepth.toFixed(2)} color={qColor(m.AvgWaitingQueueDepth)} />
      <Row label="Max Queue Depth" value={m.MaxWaitingQueueDepth.toFixed(2)} color={qColor(m.MaxWaitingQueueDepth)} />
      <Row label="Avg Running Queue" value={m.AvgRunningQueueDepth.toFixed(2)} />
      <Row label="Avg Batch Util" value={`${(m.AvgBatchUtilization * 100).toFixed(1)}%`} />
      <Row label="Avg KV Cache Util" value={`${(m.AvgKVCacheUtilization * 100).toFixed(1)}%`} color={kvColor(m.AvgKVCacheUtilization)} />
      <Row label="Max KV Cache Util" value={`${(m.MaxKVCacheUtilization * 100).toFixed(1)}%`} color={kvColor(m.MaxKVCacheUtilization)} />
      <Row label="Total TPS" value={m.TotalTokensPerSecond.toFixed(1)} />
      <Row label="Avg TTFT" value={`${m.AvgTimeToFirstTokenMs.toFixed(1)}ms`} />
      <Row label="P99 TTFT" value={`${m.P99TimeToFirstTokenMs.toFixed(1)}ms`} />
      <Row label="Measured At" value={formatTime(m.MeasuredAt)} />
      <Row label="Aggregated From" value={`${m.AggregatedFrom} replicas`} />
    </div>
  );
}

function ReplicasTab({ appID, state }: { appID: string; state: WorldState }) {
  const allReplicas = state.Replicas ? Object.values(state.Replicas) : [];
  const appReplicas = allReplicas.filter((r) => r.AppID === appID);

  const statusColor: Record<string, string> = {
    running: 'text-green-400',
    pending: 'text-yellow-400',
    draining: 'text-orange-400',
    terminated: 'text-gray-500',
  };

  if (appReplicas.length === 0) return <p className="text-gray-500 text-sm">No replicas</p>;

  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-gray-400 text-xs uppercase border-b border-gray-700">
          <th className="text-left py-2">Replica</th>
          <th className="text-left py-2">Cluster</th>
          <th className="text-left py-2">Node</th>
          <th className="text-center py-2">GPUs</th>
          <th className="text-center py-2">Status</th>
          <th className="text-right py-2">Age</th>
        </tr>
      </thead>
      <tbody>
        {appReplicas.map((r) => (
          <tr key={r.ReplicaID} className="border-b border-gray-700/50">
            <td className="py-2 font-mono text-xs">{r.ReplicaID.slice(0, 16)}</td>
            <td className="py-2">{r.ClusterID}</td>
            <td className="py-2 text-xs">{r.NodeID.slice(0, 12)}</td>
            <td className="py-2 text-center">{r.GPUs}</td>
            <td className={`py-2 text-center ${statusColor[r.Status] || 'text-gray-400'}`}>{r.Status}</td>
            <td className="py-2 text-right text-gray-400">{formatAge(r.CreatedAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function FailureDomainTab({ appID, app, state }: { appID: string; app: { FailureDomainRule: string }; state: WorldState }) {
  if (app.FailureDomainRule === 'none' || !app.FailureDomainRule) {
    return <p className="text-gray-500 text-sm">No failure domain rule configured</p>;
  }

  const allReplicas = state.Replicas ? Object.values(state.Replicas) : [];
  const appReplicas = allReplicas.filter((r: Replica) => r.AppID === appID && r.Status !== 'terminated');
  const byCluster: Record<string, number> = {};
  for (const r of appReplicas) {
    byCluster[r.ClusterID] = (byCluster[r.ClusterID] || 0) + 1;
  }

  const clusterCount = Object.keys(byCluster).length;
  const compliant = appReplicas.length < 2 || clusterCount >= 2;

  return (
    <div className="space-y-3 text-sm">
      <Row label="Rule" value={app.FailureDomainRule} />
      <Row label="Status" value={compliant ? 'Compliant' : 'Non-compliant: all replicas in single cluster'} color={compliant ? 'text-green-400' : 'text-red-400'} />
      <div className="border-t border-gray-700 pt-2 mt-2">
        {Object.entries(byCluster).map(([cluster, count]) => (
          <Row key={cluster} label={cluster} value={`${count} replicas`} />
        ))}
      </div>
    </div>
  );
}

function CacheTab({ appID, state }: { appID: string; state: WorldState }) {
  const app = state.Applications[appID];
  if (!app) return null;
  const modelID = app.ModelID;
  const locations: CacheLocation[] = state.CacheLocations?.[modelID] || [];

  const allReplicas = state.Replicas ? Object.values(state.Replicas) : [];
  const appReplicas = allReplicas.filter((r: Replica) => r.AppID === appID);

  // Group cache locations by cluster, pick highest tier
  const tierRank: Record<string, number> = { GPU_MEMORY: 3, LOCAL_NVME: 2, CLUSTER_STORAGE: 1 };
  const byCacheClusters: Record<string, { tier: string; rank: number }> = {};
  for (const loc of locations) {
    const existing = byCacheClusters[loc.ClusterID];
    const rank = tierRank[loc.Tier] || 0;
    if (!existing || rank > existing.rank) {
      byCacheClusters[loc.ClusterID] = { tier: loc.Tier, rank };
    }
  }

  // Count replicas per cluster
  const replicasByCluster: Record<string, number> = {};
  for (const r of appReplicas) {
    replicasByCluster[r.ClusterID] = (replicasByCluster[r.ClusterID] || 0) + 1;
  }

  const allClusters = new Set([...Object.keys(byCacheClusters), ...Object.keys(replicasByCluster)]);
  if (allClusters.size === 0) return <p className="text-gray-500 text-sm">No cache or replica data</p>;

  const tierColor: Record<string, string> = {
    GPU_MEMORY: 'text-green-400',
    LOCAL_NVME: 'text-yellow-400',
    CLUSTER_STORAGE: 'text-orange-400',
  };

  return (
    <div>
      <p className="text-xs text-gray-400 mb-2">Model: {modelID}</p>
      <table className="w-full text-sm">
        <thead>
          <tr className="text-gray-400 text-xs uppercase border-b border-gray-700">
            <th className="text-left py-2">Cluster</th>
            <th className="text-left py-2">Cache Tier</th>
            <th className="text-center py-2">Replicas</th>
            <th className="text-center py-2">Match</th>
          </tr>
        </thead>
        <tbody>
          {[...allClusters].sort().map((cid) => {
            const cache = byCacheClusters[cid];
            const replCount = replicasByCluster[cid] || 0;
            const cached = !!cache;
            const hasReplicas = replCount > 0;
            return (
              <tr key={cid} className="border-b border-gray-700/50">
                <td className="py-2">{cid}</td>
                <td className={`py-2 ${cached ? tierColor[cache.tier] || '' : 'text-red-400'}`}>
                  {cached ? cache.tier : 'Not cached'}
                </td>
                <td className="py-2 text-center">{replCount}</td>
                <td className="py-2 text-center">
                  {hasReplicas && cached ? (
                    <span className="text-green-400">&#10003;</span>
                  ) : hasReplicas && !cached ? (
                    <span className="text-red-400" title="Cold start risk">&#10007;</span>
                  ) : (
                    <span className="text-gray-500">-</span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Row({ label, value, color }: { label: string; value: string; color?: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-400">{label}</span>
      <span className={color || 'text-gray-200'}>{value}</span>
    </div>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (d.getFullYear() <= 1) return 'Never';
  return d.toLocaleTimeString();
}

function formatAge(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (ms < 0) return '0s';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}
