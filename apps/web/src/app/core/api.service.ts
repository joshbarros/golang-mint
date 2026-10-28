import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { Order, LogEntry, Drop } from './models';

export interface NewDrop {
  code: string;
  name: string;
  series: string;
  artist: string;
  unitsTotal: number;
  maxPerCollector: number;
  priceCents: number;
}

@Injectable({ providedIn: 'root' })
export class ApiService {
  private readonly http = inject(HttpClient);

  drops = () => firstValueFrom(this.http.get<Drop[]>('/api/drops'));
  openDrop = (body: NewDrop) => firstValueFrom(this.http.post<Drop>('/api/drops', body));

  orders = (dropCode: string) =>
    firstValueFrom(this.http.get<Order[]>('/api/orders', { params: { dropCode } }));
  book = (body: { dropCode: string; reference: string; units: number }) =>
    firstValueFrom(this.http.post<Order>('/api/orders', body));
  refund = (id: string) => firstValueFrom(this.http.post<Order>(`/api/orders/${id}/refund`, {}));
  events = (id: string) => firstValueFrom(this.http.get<LogEntry[]>(`/api/orders/${id}/events`));
}
