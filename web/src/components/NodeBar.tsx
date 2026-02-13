import type { Node } from '../types';

interface Props {
  node: Node;
}

export default function NodeBar({ node }: Props) {
  const total = node.TotalGPUs || 1;
  const allocatedPct = (node.AllocatedGPUs / total) * 100;

  const statusColor: Record<string, string> = {
    ready: 'bg-green-500',
    cordoned: 'bg-yellow-500',
    draining: 'bg-orange-500',
    down: 'bg-red-500',
  };

  return (
    <div className="flex items-center gap-3 py-1.5 px-2 bg-gray-800 rounded">
      <span
        className={`w-2 h-2 rounded-full ${statusColor[node.Status] || 'bg-gray-500'}`}
        title={node.Status}
      />
      <span className="text-sm font-mono w-40 truncate" title={node.NodeID}>
        {node.NodeID}
      </span>
      <div className="flex-1 bg-gray-700 rounded-full h-4 overflow-hidden">
        <div
          className="bg-blue-500 h-full transition-all duration-300"
          style={{ width: `${allocatedPct}%` }}
        />
      </div>
      <span className="text-xs text-gray-400 w-20 text-right">
        {node.AllocatedGPUs}/{node.TotalGPUs} GPUs
      </span>
      <span className="text-xs text-gray-500 w-16 text-right">
        {node.Pods?.length || 0} pods
      </span>
    </div>
  );
}
