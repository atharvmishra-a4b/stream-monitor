"use client";
import { useCallback, useEffect, useRef, useState } from "react";

export type State = "ok" | "problem" | "offline" | "standby";

export const stateLabel: Record<State, string> = {
  ok: "Healthy",
  problem: "Needs attention",
  offline: "Offline",
  standby: "Standby",
};

export type Counts = { total: number; ok: number; problem: number; offline: number; standby: number };
export type Schedule = { active: boolean; text: string };
export type Health = { state: State; last_seen_age: number | null; problems: string[] };
export type Hub = { id: string; name: string };
export type Temple = { id: string; hub_id: string; name: string };

export type HubRow = Hub & {
  temples: number;
  devices: Counts;
  strip: { id: string; name: string; state: State }[];
};
export type HubsResponse = { hubs: HubRow[]; totals: Counts; devices_in_unknown_temples: number; schedule: Schedule };

export type DeviceSummary = Health & {
  id: string; name: string; hostname: string; os: string; temple_id: string;
  cpu: number | null; mem: number | null; net_up: number | null;
};
export type HubDetail = {
  hub: Hub;
  temples: (Temple & { devices: DeviceSummary[] })[];
  temples_without_devices: Temple[];
  devices: Counts;
};

export type Device = {
  id: string; machine_id: string; name: string; hostname: string; temple_id: string; os: string;
  specs: Record<string, string | number>; active: boolean; created_at: string; updated_at: string;
};
export type Sample = { ts: number; cpu: number; mem: number; net_up: number; net_down: number; net_err: number; latency_ms: number };
export type DeviceDetail = { device: Device; temple: Temple; hub: Hub; health: Health; latest: Sample | null; schedule: Schedule };
export type SeriesResponse = { range: string; start: number; end: number; step: number; series: Record<string, [number, number][]> };

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api${path}`, { ...init, headers: { "Content-Type": "application/json" }, cache: "no-store" });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error ?? `Request failed (${res.status})`);
  return body as T;
}

// Fetches on a timer and keeps the last good data on screen if a refresh fails.
export function usePoll<T>(path: string | null, everyMs = 10000) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const alive = useRef(true);

  const load = useCallback(async () => {
    if (!path) return;
    try {
      const next = await api<T>(path);
      if (!alive.current) return;
      setData(next); setError(null); setUpdatedAt(Date.now());
    } catch (e) {
      if (alive.current) setError(e instanceof Error ? e.message : "Something went wrong");
    }
  }, [path]);

  useEffect(() => {
    alive.current = true;
    setData(null); setError(null);
    load();
    const t = setInterval(() => { if (!document.hidden) load(); }, everyMs);
    return () => { alive.current = false; clearInterval(t); };
  }, [load, everyMs]);

  return { data, error, updatedAt, refresh: load };
}

// ---- formatting ----
export const pct = (v: number | null | undefined) => (v == null ? "–" : `${Math.round(v)}%`);
export const ms = (v: number | null | undefined) => (v == null ? "–" : v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${Math.round(v)} ms`);
export function bps(v: number | null | undefined) {
  if (v == null) return "–";
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)} Mbps`;
  return `${Math.round(v / 1e3)} kbps`;
}
export function ago(seconds: number | null | undefined) {
  if (seconds == null) return "never";
  if (seconds < 90) return `${Math.max(0, Math.round(seconds))}s ago`;
  if (seconds < 5400) return `${Math.round(seconds / 60)} min ago`;
  return `${Math.round(seconds / 3600)} h ago`;
}
export const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;
