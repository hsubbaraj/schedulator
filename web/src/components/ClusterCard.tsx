import { useState } from 'react';
import type { Cluster, GPUReservation, CacheLocation } from '../types';
import NodeBar from './NodeBar';

interface Props {
  cluster: Cluster;
  reservations: GPUReservation[];
  cacheLocations: Record<string, CacheLocation[]>;
}

export default function ClusterCard({ cluster, reservations, cacheLocations }: Props) {
  const [expanded, setExpanded] = useState(false);

  const allNodes = cluster.Nodes ? Object.values(cluster.Nodes) : [];
  // Filter out non-GPU nodes (e.g., control-plane nodes with 0 GPUs).
  const nodes = allNodes.filter((n) => n.TotalGPUs > 0);
  const totalGPUs = nodes.reduce((sum, n) => sum + n.TotalGPUs, 0);
  const allocatedGPUs = nodes.reduce((sum, n) => sum + n.AllocatedGPUs, 0);
  const freeGPUs = totalGPUs - allocatedGPUs;
  const readyNodes = nodes.filter((n) => n.Status === 'ready').length;

  const utilizationPct = totalGPUs > 0 ? Math.round((allocatedGPUs / totalGPUs) * 100) : 0;

  // Reservations for this cluster
  const clusterReservations = reservations.filter(
    (r) => r.ClusterID === cluster.ClusterID && r.Status === 'active'
  );

  // Count cached models for this cluster
  let cachedModelCount = 0;
  for (const locations of Object.values(cacheLocations)) {
    if (locations.some((loc) => loc.ClusterID === cluster.ClusterID)) {
      cachedModelCount++;
    }
  }

  // Compute reserved GPUs per node for NodeBar
  const reservedByNode: Record<string, number> = {};
  for (const r of clusterReservations) {
    if (r.NodeID) {
      reservedByNode[r.NodeID] = (reservedByNode[r.NodeID] || 0) + r.GPUs;
    }
  }

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
        <div className="mt-2 flex gap-2">
          {clusterReservations.length > 0 && (
            <span className="text-xs px-2 py-0.5 rounded-full bg-yellow-900/50 text-yellow-400 border border-yellow-700">
              {clusterReservations.length} reserved
            </span>
          )}
          {cachedModelCount > 0 && (
            <span className="text-xs px-2 py-0.5 rounded-full bg-blue-900/50 text-blue-400 border border-blue-700">
              {cachedModelCount} models cached
            </span>
          )}
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
              .map((node) => (
                <NodeBar key={node.NodeID} node={node} reservedGPUs={reservedByNode[node.NodeID] || 0} />
              ))
          )}
        </div>
      )}
    </div>
  );
}
