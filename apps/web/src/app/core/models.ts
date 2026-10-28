export interface Drop {
  id: string;
  code: string;
  name: string;
  series: string;
  artist: string;
  releaseDate: string;
  priceCents: number;
  unitsTotal: number;
  unitsLeft: number;
  maxPerCollector: number;
}

export type OrderStatus = 'Requested' | 'Held' | 'Sold' | 'Refunded' | 'Rejected';

export interface Order {
  id: string;
  dropCode: string;
  ownerId: string;
  reference: string;
  units: number;
  totalCents: number;
  status: OrderStatus;
  reason: string | null;
  certificateNumber: string | null;
  createdAt: string;
  updatedAt: string;
  unitsReleasedAt: string | null;
}

export interface LogEntry {
  id: string;
  type: string;
  module: string;
  occurredAt: string;
}

export interface UserInfo {
  id: string;
  email: string;
  roles: string[];
}

export interface AuthResponse {
  accessToken: string;
  expiresAt: string;
  user: UserInfo;
}
