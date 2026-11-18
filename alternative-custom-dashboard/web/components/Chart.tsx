"use client";
import { useState } from "react";

type Point = [number, number];

// A small dependency-free line chart. Gaps in the data (laptop offline) are drawn as gaps, not interpolated.
export function Chart({ title, points, step, format, min = 0, max, limit, empty }: {
  title: string; points: Point[]; step: number; format: (v: number) => string;
  min?: number; max?: number; limit?: { value: number; label: string }; empty: string;
}) {
  const W = 640, H = 170, L = 8, R = 8, T = 12, B = 22;
  const [hover, setHover] = useState<number | null>(null);

  if (points.length === 0) {
    return (
      <figure className="chart">
        <figcaption>{title}</figcaption>
        <p className="chart-empty">{empty}</p>
      </figure>
    );
  }
  const t0 = points[0][0], t1 = points[points.length - 1][0];
  const dataMax = Math.max(...points.map((p) => p[1]));
  const top = max ?? Math.max(dataMax * 1.15, min + 1);
  const x = (t: number) => L + ((t - t0) / Math.max(t1 - t0, 1)) * (W - L - R);
  const y = (v: number) => T + (1 - (v - min) / (top - min)) * (H - T - B);

  // split into runs wherever consecutive samples are further apart than 2.5 steps
  const runs: Point[][] = [];
  for (const p of points) {
    const run = runs[runs.length - 1];
    if (run && p[0] - run[run.length - 1][0] <= step * 2.5) run.push(p); else runs.push([p]);
  }
  const path = (run: Point[]) => run.map((p, i) => `${i ? "L" : "M"}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join(" ");
  const last = points[points.length - 1];
  const shown = hover != null ? points[hover] : last;
  const clock = (t: number) => new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

  function onMove(e: React.PointerEvent<SVGSVGElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    const t = t0 + ((e.clientX - box.left) / box.width) * (t1 - t0);
    let best = 0;
    points.forEach((p, i) => { if (Math.abs(p[0] - t) < Math.abs(points[best][0] - t)) best = i; });
    setHover(best);
  }

  return (
    <figure className="chart">
      <figcaption>
        <span>{title}</span>
        <span className="chart-value">{format(shown[1])}<span className="chart-time">{hover != null ? ` at ${clock(shown[0])}` : " now"}</span></span>
      </figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={`${title}, latest ${format(last[1])}`}
           onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
        <line className="axis" x1={L} x2={W - R} y1={H - B} y2={H - B} />
        <line className="grid" x1={L} x2={W - R} y1={y(top)} y2={y(top)} />
        <text className="tick" x={L} y={T - 2}>{format(top)}</text>
        {limit && limit.value < top && (
          <>
            <line className="limit" x1={L} x2={W - R} y1={y(limit.value)} y2={y(limit.value)} />
            <text className="tick limit-text" x={W - R} y={y(limit.value) - 4} textAnchor="end">{limit.label}</text>
          </>
        )}
        {runs.map((run, i) => run.length > 1
          ? <path key={i} d={path(run)} className="line" />
          : <circle key={i} cx={x(run[0][0])} cy={y(run[0][1])} r={2} className="dotpt" />)}
        {hover != null && <circle cx={x(shown[0])} cy={y(shown[1])} r={4} className="dotpt hot" />}
        <text className="tick" x={L} y={H - 6}>{clock(t0)}</text>
        <text className="tick" x={W - R} y={H - 6} textAnchor="end">{clock(t1)}</text>
      </svg>
    </figure>
  );
}
