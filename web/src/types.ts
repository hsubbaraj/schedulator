export interface Node {
  NodeID: string;
  ClusterID: string;
  TotalGPUs: number;
  AllocatedGPUs: number;
  FreeGPUs: number;
  Pods: string[] | null;
  CachedModels: Record<string, object> | null;
  Status: string;
}

export interface Cluster {
  ClusterID: string;
  Nodes: Record<string, Node> | null;
  FragmentationScore: number;
}

export interface Application {
  AppID: string;
  ModelID: string;
  GPUsPerReplica: number;
  Priority: number;
  MinReplicas: number;
  FailureDomainRule: string;
}

export interface Replica {
  ReplicaID: string;
  AppID: string;
  ClusterID: string;
  NodeID: string;
  GPUs: number;
  Status: string;
  CreatedAt: string;
}

export interface WorldState {
  Clusters: Record<string, Cluster>;
  Applications: Record<string, Application>;
  Replicas: Record<string, Replica>;
  TakenAt: string;
}

export interface EventRecord {
  id: number;
  timestamp: string;
  type: string;
  app_id: string;
  cluster_id: string;
  summary: string;
  detail_json: string;
}

export interface ClusterSnapshot {
  id: number;
  timestamp: string;
  cluster_id: string;
  total_gpus: number;
  allocated_gpus: number;
  free_gpus: number;
  replica_count: number;
}

export interface SSEEvent {
  type: string;
  timestamp: string;
  data: unknown;
}
