import { useState } from 'react';
import type { Cluster } from '../types';
import NodeBar from './NodeBar';

interface Props {
  cluster: Cluster;
}

export default function ClusterCard({ cluster }: Props) {
  const [expanded, setExpanded] = useState(false);

  const nodes = cluster.Nodes ? Object.values(cluster.Nodes) : [];
  const totalGPUs = nodes.reduce((sum, n) => sum + n.TotalGPUs, 0);
  const allocatedGPUs = nodes.reduce((sum, n) => sum + n.AllocatedGPUs, 0);
  const freeGPUs = totalGPUs - allocatedGPUs;
  const readyNodes = nodes.filter((n) => n.Status === 'ready').length;

  const utilizationPct = totalGPUs > 0 ? Math.round((allocatedGPUs / totalGPUs) * 100) : 0;

  return (
    <div className="bg-gray-800 rounded-lg border border-gray-700 overflow-hidden">
      <button
        className="w-full p-4 text-left hover:bg-gray-750 transition-colors"
        onClick={() => setExpanded(!expanded)}
      >
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-lg font-semibold">{cluster.ClusterID}</h3>
            <p className="text-sm text-gray-400">
              {readyNodes}/{nodes.length} nodes ready
            </p>
          </div>
          <div className="text-right">
            <div className="text-2xl font-bold">{utilizationPct}%</div>
            <p className="text-sm text-gray-400">
              {allocatedGPUs}/{totalGPUs} GPUs ({freeGPUs} free)
            </p>
          </div>
        </div>
        <div className="mt-2 bg-gray-700 rounded-full h-2 overflow-hidden">
          <div
            className={`h-full transition-all duration-300 ${
              utilizationPct > 80 ? 'bg-red-500' : utilizationPct > 50 ? 'bg-yellow-500' : 'bg-green-500'
            }`}
            style={{ width: `${utilizationPct}%` }}
          />
        </div>
        <p className="text-xs text-gray-500 mt-1">
          Fragmentation: {(cluster.FragmentationScore * 100).toFixed(1)}%
        </p>
      </button>

      {expanded && (
        <div className="px-4 pb-4 space-y-1 border-t border-gray-700 pt-2">
          {nodes.length === 0 ? (
            <p className="text-sm text-gray-500">No nodes</p>
          ) : (
            nodes
              .sort((a, b) => a.NodeID.localeCompare(b.NodeID))
              .map((node) => <NodeBar key={node.NodeID} node={node} />)
          )}
        </div>
      )}
    </div>
  );
}
