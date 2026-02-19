import { useMemo } from 'react';
import type { EventRecord, CycleSummary, ScalingDecision } from '../types';

interface Props {
  event: EventRecord;
  onClose: () => void;
}

export default function ControlLoopDetailModal({ event, onClose }: Props) {
  const summary = useMemo(() => {
    try {
      return JSON.parse(event.detail_json) as CycleSummary;
    } catch {
      return null;
    }
  }, [event.detail_json]);

  if (!summary) return null;

  const { world_state, scaling_decisions, placement, cycle_duration_ms, trigger_kinds } = summary;

  const groupedScaleUps = useMemo(() => {
    const groups: Record<string, { appID: string; clusterID: string; count: number }> = {};
    placement.scale_ups?.forEach(su => {
      const key = `${su.AppID}|${su.ClusterID}`;
      if (!groups[key]) {
        groups[key] = { appID: su.AppID, clusterID: su.ClusterID, count: 0 };
      }
      groups[key].count++;
    });
    return Object.values(groups).sort((a, b) => a.appID.localeCompare(b.appID) || a.clusterID.localeCompare(b.clusterID));
  }, [placement.scale_ups]);

  const groupedScaleDowns = useMemo(() => {
    const groups: Record<string, { appID: string; clusterID: string; count: number }> = {};
    placement.scale_downs?.forEach(sd => {
      const replica = world_state.Replicas[sd.ReplicaID];
      const clusterID = replica?.ClusterID || 'unknown';
      const key = `${sd.AppID}|${clusterID}`;
      if (!groups[key]) {
        groups[key] = { appID: sd.AppID, clusterID: clusterID, count: 0 };
      }
      groups[key].count++;
    });
    return Object.values(groups).sort((a, b) => a.appID.localeCompare(b.appID) || a.clusterID.localeCompare(b.clusterID));
  }, [placement.scale_downs, world_state.Replicas]);

  const groupedPreemptions = useMemo(() => {
    const groups: Record<string, { appID: string; clusterID: string; count: number; reason: string }> = {};
    placement.preemptions?.forEach(p => {
      const key = `${p.VictimAppID}|${p.ClusterID}|${p.Reason}`;
      if (!groups[key]) {
        groups[key] = { appID: p.VictimAppID, clusterID: p.ClusterID, count: 0, reason: p.Reason };
      }
      groups[key].count++;
    });
    return Object.values(groups).sort((a, b) => a.appID.localeCompare(b.appID) || a.clusterID.localeCompare(b.clusterID));
  }, [placement.preemptions]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-5xl max-h-[90vh] flex flex-col shadow-2xl">
        {/* Header */}
        <div className="px-6 py-4 border-b border-gray-800 flex justify-between items-center bg-gray-900 rounded-t-xl">
          <div>
            <h2 className="text-xl font-bold text-gray-100 flex items-center gap-3">
              Cycle Detail: #{event.id}
              <span className="text-sm font-normal text-gray-400">
                {new Date(event.timestamp).toLocaleString()}
              </span>
            </h2>
            <div className="flex gap-2 mt-1">
              {trigger_kinds.map((kind) => (
                <span key={kind} className="text-[10px] uppercase font-bold px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400 border border-blue-500/30">
                  {kind}
                </span>
              ))}
              <span className="text-[10px] uppercase font-bold px-1.5 py-0.5 rounded bg-gray-800 text-gray-400 border border-gray-700">
                {cycle_duration_ms}ms duration
              </span>
            </div>
          </div>
          <button
            onClick={onClose}
            className="p-2 hover:bg-gray-800 rounded-lg transition-colors text-gray-400 hover:text-gray-200"
          >
            <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-y-auto p-6 space-y-8">
          {/* Scaling Decisions */}
          <section>
            <h3 className="text-sm font-bold text-gray-400 uppercase tracking-wider mb-4">Scaling Decisions</h3>
            <div className="bg-gray-950 border border-gray-800 rounded-lg overflow-hidden">
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-gray-800 text-gray-500 uppercase">
                    <th className="text-left p-2">App ID</th>
                    <th className="text-center p-2">Current</th>
                    <th className="text-center p-2">Target</th>
                    <th className="text-center p-2">Direction</th>
                    <th className="text-center p-2">Signal</th>
                    <th className="text-center p-2">Approved</th>
                  </tr>
                </thead>
                <tbody>
                  {Object.values(scaling_decisions).map((d: ScalingDecision) => (
                    <tr key={d.AppID} className="border-b border-gray-800/50 last:border-0">
                      <td className="p-2 font-mono text-blue-400">{d.AppID}</td>
                      <td className="p-2 text-center text-gray-300">{d.CurrentCount}</td>
                      <td className="p-2 text-center text-gray-100 font-bold">{d.TargetCount}</td>
                      <td className="p-2 text-center">
                        <span className={`px-1.5 py-0.5 rounded font-bold uppercase text-[9px] ${
                          d.Direction === 'up' ? 'bg-green-500/20 text-green-400' :
                          d.Direction === 'down' ? 'bg-orange-500/20 text-orange-400' :
                          'bg-gray-800 text-gray-500'
                        }`}>
                          {d.Direction}
                        </span>
                      </td>
                      <td className="p-2 text-center text-gray-400">{d.Signal}</td>
                      <td className="p-2 text-center">
                        {d.ScaleActionApproved ? (
                          <span className="text-green-500">✓</span>
                        ) : d.Direction !== 'unchanged' ? (
                          <span className="text-gray-600" title="Cooldown or stabilization">×</span>
                        ) : '-'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          {/* Placement Changes */}
          <section className="grid grid-cols-1 md:grid-cols-3 gap-6">
            <div>
              <h3 className="text-sm font-bold text-gray-400 uppercase tracking-wider mb-3">Scale Ups ({placement.scale_up_count})</h3>
              <div className="space-y-2">
                {groupedScaleUps.length > 0 ? groupedScaleUps.map((g, i) => (
                  <div key={i} className="bg-gray-950 border border-green-900/30 p-2 rounded text-xs flex justify-between items-center">
                    <div className="flex flex-col">
                      <span className="font-medium text-gray-200">{g.appID}</span>
                      <span className="text-[10px] text-gray-500">{g.clusterID}</span>
                    </div>
                    <span className="text-green-400 font-bold bg-green-400/10 px-2 py-1 rounded">+{g.count}</span>
                  </div>
                )) : <div className="text-gray-600 text-xs italic">No scale ups</div>}
              </div>
            </div>
            <div>
              <h3 className="text-sm font-bold text-gray-400 uppercase tracking-wider mb-3">Scale Downs ({placement.scale_down_count})</h3>
              <div className="space-y-2">
                {groupedScaleDowns.length > 0 ? groupedScaleDowns.map((g, i) => (
                  <div key={i} className="bg-gray-950 border border-orange-900/30 p-2 rounded text-xs flex justify-between items-center">
                    <div className="flex flex-col">
                      <span className="font-medium text-gray-200">{g.appID}</span>
                      <span className="text-[10px] text-gray-500">{g.clusterID}</span>
                    </div>
                    <span className="text-orange-400 font-bold bg-orange-400/10 px-2 py-1 rounded">-{g.count}</span>
                  </div>
                )) : <div className="text-gray-600 text-xs italic">No scale downs</div>}
              </div>
            </div>
            <div>
              <h3 className="text-sm font-bold text-gray-400 uppercase tracking-wider mb-3">Preemptions ({placement.preemption_count})</h3>
              <div className="space-y-2">
                {groupedPreemptions.length > 0 ? groupedPreemptions.map((g, i) => (
                  <div key={i} className="bg-gray-950 border border-red-900/30 p-2 rounded text-xs">
                    <div className="flex justify-between items-center mb-1">
                      <div className="flex flex-col">
                        <span className="font-medium text-red-400">{g.appID}</span>
                        <span className="text-[10px] text-gray-500">{g.clusterID}</span>
                      </div>
                      <span className="text-red-400 font-bold bg-red-400/10 px-2 py-1 rounded">×{g.count}</span>
                    </div>
                    <div className="text-gray-500 text-[10px] truncate leading-tight border-t border-gray-900 pt-1 mt-1">{g.reason}</div>
                  </div>
                )) : <div className="text-gray-600 text-xs italic">No preemptions</div>}
              </div>
            </div>
          </section>

          {/* Input World State */}
          <section>
             <h3 className="text-sm font-bold text-gray-400 uppercase tracking-wider mb-4">Input World State Snapshot</h3>
             <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
               {/* Clusters Summary */}
               <div className="bg-gray-950 border border-gray-800 rounded-lg p-4">
                 <h4 className="text-xs font-bold text-gray-500 uppercase mb-3">Clusters Overview</h4>
                 <div className="space-y-3">
                   {Object.values(world_state.Clusters).map(cluster => {
                     const nodes = Object.values(cluster.Nodes || {});
                     const totalGPUs = nodes.reduce((sum, n) => sum + n.TotalGPUs, 0);
                     const freeGPUs = nodes.reduce((sum, n) => sum + n.FreeGPUs, 0);
                     const usage = totalGPUs > 0 ? ((totalGPUs - freeGPUs) / totalGPUs) * 100 : 0;
                     return (
                       <div key={cluster.ClusterID} className="space-y-1">
                         <div className="flex justify-between text-xs">
                           <span className="font-medium text-gray-300">{cluster.ClusterID}</span>
                           <span className="text-gray-500">{totalGPUs - freeGPUs} / {totalGPUs} GPUs</span>
                         </div>
                         <div className="w-full bg-gray-800 h-1.5 rounded-full overflow-hidden">
                           <div className="bg-blue-500 h-full" style={{ width: `${usage}%` }} />
                         </div>
                       </div>
                     );
                   })}
                 </div>
               </div>

               {/* Apps Summary */}
               <div className="bg-gray-950 border border-gray-800 rounded-lg p-4">
                 <h4 className="text-xs font-bold text-gray-500 uppercase mb-3">Active Replicas</h4>
                 <div className="grid grid-cols-2 gap-x-4 gap-y-2">
                   {Object.values(world_state.Applications).map(app => {
                     const replicas = Object.values(world_state.Replicas).filter(r => r.AppID === app.AppID);
                     return (
                       <div key={app.AppID} className="flex justify-between items-center text-xs border-b border-gray-800/50 pb-1">
                         <span className="text-gray-400 truncate mr-2">{app.AppID}</span>
                         <span className="text-gray-200 font-mono">{replicas.length}</span>
                       </div>
                     );
                   })}
                 </div>
               </div>
             </div>
          </section>
        </div>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-gray-800 bg-gray-900/50 rounded-b-xl flex justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 bg-gray-800 hover:bg-gray-700 text-gray-200 rounded-lg text-sm font-medium transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
