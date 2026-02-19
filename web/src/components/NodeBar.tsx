import type { Node } from '../types';

interface Props {
  node: Node;
  reservedGPUs?: number;
}

export default function NodeBar({ node, reservedGPUs = 0 }: Props) {
  const total = node.TotalGPUs || 1;
  const allocated = node.AllocatedGPUs;
  const reserved = Math.min(reservedGPUs, total - allocated);
  const free = total - allocated - reserved;

  const statusColor: Record<string, string> = {
    ready: 'bg-green-500',
    cordoned: 'bg-yellow-500',
    draining: 'bg-orange-500',
    down: 'bg-red-500',
  };

  // Render individual GPU slots
  const slots: ('allocated' | 'reserved' | 'free')[] = [];
  for (let i = 0; i < allocated; i++) slots.push('allocated');
  for (let i = 0; i < reserved; i++) slots.push('reserved');
  for (let i = 0; i < free; i++) slots.push('free');

  const slotColor: Record<string, string> = {
    allocated: 'bg-blue-500',
    reserved: 'bg-yellow-500 bg-stripes',
    free: 'bg-gray-600',
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
      <div className="flex-1 flex gap-0.5">
        {slots.map((type, i) => (
          <div
            key={i}
            className={`h-4 flex-1 rounded-sm ${slotColor[type]}`}
            title={type}
          />
        ))}
      </div>
      <span className="text-xs text-gray-400 w-24 text-right">
        {allocated}/{total} GPUs
        {reserved > 0 && <span className="text-yellow-400"> +{reserved}r</span>}
      </span>
      <span className="text-xs text-gray-500 w-16 text-right">
        {node.Pods?.length || 0} pods
      </span>
    </div>
  );
}
