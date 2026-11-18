"use client";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { DeviceDetail, SeriesResponse, Temple, api, bps, ms, ago, pct, usePoll } from "@/lib/api";
import { Loading, Shell } from "@/components/Shell";
import { StateMark } from "@/components/StateMark";
import { Chart } from "@/components/Chart";

const SPEC_ORDER: [string, string][] = [
  ["processor", "Processor"], ["cores", "Cores"], ["ram_gb", "Memory"], ["os_version", "Operating system"],
];
const RANGES = [["15m", "15 minutes"], ["1h", "1 hour"], ["6h", "6 hours"], ["24h", "24 hours"]] as const;

function Figure({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="figure">
      <dt>{label}</dt>
      <dd>{value}{note && <span className="muted small"> {note}</span>}</dd>
    </div>
  );
}

function Manage({ detail, onChanged }: { detail: DeviceDetail; onChanged: () => void }) {
  const router = useRouter();
  const { device } = detail;
  const [name, setName] = useState(device.name);
  const [temple, setTemple] = useState(device.temple_id);
  const [temples, setTemples] = useState<(Temple & { hub_name: string })[]>([]);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => { api<{ temples: (Temple & { hub_name: string })[] }>("/temples").then((r) => setTemples(r.temples)).catch(() => {}); }, []);

  async function save(body: object, done: string) {
    setBusy(true); setMsg(null);
    try {
      await api(`/devices/${device.id}`, { method: "PATCH", body: JSON.stringify(body) });
      setMsg({ ok: true, text: done });
      onChanged();
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : "Could not save." });
    } finally { setBusy(false); }
  }
  const dirty = name.trim() !== device.name || temple !== device.temple_id;

  return (
    <section className="manage">
      <h2>Manage this device</h2>
      <div className="form">
        <label>Name<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label>Temple
          <select value={temple} onChange={(e) => setTemple(e.target.value)}>
            {!temples.some((t) => t.id === device.temple_id) && <option value={device.temple_id}>{detail.temple.name}</option>}
            {temples.map((t) => <option key={t.id} value={t.id}>{t.hub_name}: {t.name}</option>)}
          </select>
        </label>
        <div className="actions">
          <button disabled={!dirty || busy} onClick={() => save({ name: name.trim(), temple_id: temple }, "Saved.")}>Save changes</button>
          <button className="danger" disabled={busy} onClick={() => {
            if (confirm(`Stop monitoring ${device.name}? It will disappear from the dashboard until its agent registers again.`))
              api(`/devices/${device.id}`, { method: "PATCH", body: JSON.stringify({ active: false }) }).then(() => router.push(`/hubs/${detail.hub.id}`));
          }}>Stop monitoring</button>
        </div>
        {msg && <p className={msg.ok ? "ok-text" : "is-bad"} role="status">{msg.text}</p>}
      </div>
    </section>
  );
}

export default function DevicePage() {
  const { id } = useParams<{ id: string }>();
  const [range, setRange] = useState<string>("1h");
  const dev = usePoll<DeviceDetail>(`/devices/${id}`, 10000);
  const ser = usePoll<SeriesResponse>(`/devices/${id}/series?range=${range}`, 20000);
  const d = dev.data;
  const n = d?.latest ?? null;
  const isStandby = d?.health.state === "standby";
  const offline = d?.health.state === "offline" || isStandby; // no live figures in either case
  const error = dev.error;

  return (
    <Shell
      crumbs={d ? [{ label: d.hub.name, href: `/hubs/${d.hub.id}` }, { label: d.temple.name }, { label: d.device.name }] : [{ label: "Device" }]}
      error={error} updatedAt={dev.updatedAt}>
      {!d ? (error ? null : <Loading what="device" />) : (
        <>
          <section className="headline">
            <h1>{d.device.name}</h1>
            <p><StateMark state={d.health.state} />
              {offline && d.health.last_seen_age != null && <span className="muted"> · last heard {ago(d.health.last_seen_age)}</span>}</p>
            {d.health.problems.length > 0 && (
              <ul className="problems">{d.health.problems.map((p) => <li key={p}>{p}</li>)}</ul>
            )}
            {isStandby && <p className="muted">This laptop is not reporting, which is expected outside streaming hours. {d.schedule.text}.</p>}
            {offline && !isStandby && <p className="muted">Check that the laptop is on, connected to the internet and that the agent is running. Figures below are blank until it reports again.</p>}
          </section>

          <section>
            <h2>Right now</h2>
            <dl className="figures">
              <Figure label="CPU" value={offline || !n ? "–" : pct(n.cpu)} />
              <Figure label="Memory" value={offline || !n ? "–" : pct(n.mem)} />
              <Figure label="Upload" value={offline || !n ? "–" : bps(n.net_up)} />
              <Figure label="Download" value={offline || !n ? "–" : bps(n.net_down)} />
              <Figure label="Errors and drops" value={offline || !n ? "–" : `${n.net_err.toFixed(1)} per min`} />
              <Figure label="Connection delay" value={offline || !n ? "–" : ms(n.latency_ms)} note="per report" />
            </dl>
          </section>

          <section>
            <div className="sectionbar">
              <h2>History</h2>
              <div className="seg" role="group" aria-label="Time range">
                {RANGES.map(([v, label]) => (
                  <button key={v} aria-pressed={range === v} onClick={() => setRange(v)}>{label}</button>
                ))}
              </div>
            </div>
            {ser.error && !ser.data ? <p className="is-bad">{ser.error}</p> : !ser.data ? <Loading what="charts" /> : (
              <div className="charts">
                <Chart title="CPU" points={ser.data.series.cpu ?? []} step={ser.data.step} format={(v) => `${Math.round(v)}%`} max={100} limit={{ value: 90, label: "90%" }} empty="No data in this window." />
                <Chart title="Memory" points={ser.data.series.memory ?? []} step={ser.data.step} format={(v) => `${Math.round(v)}%`} max={100} limit={{ value: 93, label: "93%" }} empty="No data in this window." />
                <Chart title="Upload" points={ser.data.series.net_up_bps ?? []} step={ser.data.step} format={bps} empty="No data in this window." />
                <Chart title="Download" points={ser.data.series.net_down_bps ?? []} step={ser.data.step} format={bps} empty="No data in this window." />
                <Chart title="Connection delay" points={ser.data.series.latency_ms ?? []} step={ser.data.step} format={ms} limit={{ value: 1500, label: "1.5 s" }} empty="No data in this window." />
                <Chart title="Network errors per minute" points={ser.data.series.net_errors ?? []} step={ser.data.step} format={(v) => v.toFixed(1)} limit={{ value: 10, label: "10" }} empty="No data in this window." />
              </div>
            )}
          </section>

          <section>
            <h2>Hardware</h2>
            <dl className="specs">
              {SPEC_ORDER.filter(([k]) => d.device.specs[k] !== undefined && d.device.specs[k] !== "").map(([k, label]) => (
                <div key={k}><dt>{label}</dt><dd>{String(d.device.specs[k])}{k === "ram_gb" ? " GB" : ""}</dd></div>
              ))}
              <div><dt>Hostname</dt><dd>{d.device.hostname || "–"}</dd></div>
              <div><dt>Registered</dt><dd>{new Date(d.device.created_at).toLocaleDateString()}</dd></div>
              <div><dt>Device id</dt><dd className="mono">{d.device.id}</dd></div>
            </dl>
          </section>

          <Manage detail={d} onChanged={dev.refresh} />
          <p className="small"><Link href={`/hubs/${d.hub.id}`}>Back to {d.hub.name}</Link></p>
        </>
      )}
    </Shell>
  );
}
