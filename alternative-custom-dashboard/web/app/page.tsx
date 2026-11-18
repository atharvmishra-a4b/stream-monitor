"use client";
import Link from "next/link";
import { Counts, HubRow, HubsResponse, Schedule, plural, usePoll } from "@/lib/api";
import { Loading, Shell } from "@/components/Shell";

function headline(t: Counts, sched: Schedule): { text: string; sub: string; tone: "ok" | "bad" | "none" } {
  if (t.total === 0) return { text: "No devices registered yet", sub: "Run the agent on a laptop and it will appear here.", tone: "none" };
  const bad = t.problem + t.offline;
  const parts = [t.ok && `${t.ok} healthy`, t.problem && `${t.problem} with problems`, t.offline && `${t.offline} offline`, t.standby && `${t.standby} on standby`].filter(Boolean);
  if (bad > 0) return { text: `${plural(bad, "device")} ${bad === 1 ? "needs" : "need"} attention`, sub: `${parts.join(", ")}. ${sched.text}.`, tone: "bad" };
  if (t.ok === 0) return { text: "No stream right now", sub: `${sched.text}. Laptops are off or asleep between streams, as expected.`, tone: "none" };
  return { text: `All ${plural(t.ok, "device")} healthy`, sub: `${t.standby ? `${t.standby} on standby. ` : ""}${sched.text}.`, tone: "ok" };
}

function HubLine({ hub }: { hub: HubRow }) {
  const d = hub.devices;
  const bad = d.problem + d.offline;
  return (
    <li className="hubline">
      <Link href={`/hubs/${hub.id}`} className="hubline-link">
        <div className="hubline-name">
          <h2>{hub.name}</h2>
          <span className="muted">
            {plural(hub.temples, "temple")}
            {d.total > 0 ? `, ${plural(d.total, "device")}` : ", no devices yet"}
          </span>
        </div>
        <div className="strip" role="img" aria-label={`${plural(d.total, "device")}: ${d.ok} healthy, ${d.problem} with problems, ${d.offline} offline, ${d.standby} on standby`}>
          {hub.strip.map((c) => <span key={c.id} className={`cell s-${c.state}`} title={c.name} />)}
        </div>
        <div className={`hubline-status ${bad > 0 ? "is-bad" : ""}`}>
          {d.total === 0 ? <span className="muted">–</span>
            : d.standby === d.total ? <span className="muted">Standby</span>
            : bad > 0 ? <>{d.problem > 0 && <span>{d.problem} with problems</span>}{d.offline > 0 && <span>{d.offline} offline</span>}</>
            : <span>All healthy</span>}
        </div>
      </Link>
    </li>
  );
}

export default function Home() {
  const { data, error, updatedAt } = usePoll<HubsResponse>("/hubs");
  const h = data ? headline(data.totals, data.schedule) : null;
  return (
    <Shell crumbs={[]} error={error} updatedAt={updatedAt}>
      {!data ? (error ? null : <Loading what="hubs" />) : (
        <>
          <section className="headline">
            <h1 className={h!.tone === "bad" ? "is-bad" : ""}>{h!.text}</h1>
            <p className="muted">{h!.sub}</p>
          </section>
          {data.devices_in_unknown_temples > 0 && (
            <p className="notice">
              {plural(data.devices_in_unknown_temples, "device")} {data.devices_in_unknown_temples === 1 ? "is" : "are"} registered to a temple that no longer exists in kratos, so
              {data.devices_in_unknown_temples === 1 ? " it is" : " they are"} not listed under any hub.
            </p>
          )}
          <ul className="hublist">{data.hubs.map((hub) => <HubLine key={hub.id} hub={hub} />)}</ul>
        </>
      )}
    </Shell>
  );
}
