"use client";
import Link from "next/link";
import { useParams } from "next/navigation";
import { HubDetail, DeviceSummary, bps, ago, pct, plural, usePoll } from "@/lib/api";
import { Loading, Shell } from "@/components/Shell";
import { StateMark } from "@/components/StateMark";

function DeviceRow({ d }: { d: DeviceSummary }) {
  return (
    <li>
      <Link href={`/devices/${d.id}`} className="devrow">
        <div className="devrow-main">
          <StateMark state={d.state} text={false} />
          <div>
            <strong>{d.name}</strong>
            <div className="muted small">
              {d.state === "standby" ? `Not reporting, which is normal outside streaming hours` : d.state === "offline" ? `Last heard ${ago(d.last_seen_age)}` : d.problems.length ? d.problems.join(". ") : "Healthy"}
            </div>
          </div>
        </div>
        <dl className="devrow-nums">
          <div><dt>CPU</dt><dd>{pct(d.cpu)}</dd></div>
          <div><dt>Memory</dt><dd>{pct(d.mem)}</dd></div>
          <div><dt>Upload</dt><dd>{bps(d.net_up)}</dd></div>
        </dl>
      </Link>
    </li>
  );
}

export default function HubPage() {
  const { id } = useParams<{ id: string }>();
  const { data, error, updatedAt } = usePoll<HubDetail>(`/hubs/${id}`);
  const bad = data ? data.devices.problem + data.devices.offline : 0;
  return (
    <Shell crumbs={[{ label: data?.hub.name ?? "Hub" }]} error={error} updatedAt={updatedAt}>
      {!data ? (error ? null : <Loading what="hub" />) : (
        <>
          <section className="headline">
            <h1>{data.hub.name}</h1>
            <p className={`muted ${bad ? "is-bad" : ""}`}>
              {data.devices.total === 0 ? "No devices registered in this hub yet."
                : bad ? `${plural(bad, "device")} ${bad === 1 ? "needs" : "need"} attention out of ${data.devices.total}`
                : data.devices.standby === data.devices.total ? "All on standby until the next stream"
                : `All ${plural(data.devices.total, "device")} healthy`}
            </p>
          </section>
          {data.temples.map((t) => (
            <section key={t.id} className="temple">
              <h2>{t.name}</h2>
              <ul className="devlist">{t.devices.map((d) => <DeviceRow key={d.id} d={d} />)}</ul>
            </section>
          ))}
          {data.temples_without_devices.length > 0 && (
            <section className="temple quiet">
              <h2>Temples with no devices</h2>
              <p className="muted">{data.temples_without_devices.map((t) => t.name).join(", ")}</p>
              <p className="muted small">Run the agent on a laptop with one of these temple ids to bring it into monitoring.</p>
            </section>
          )}
        </>
      )}
    </Shell>
  );
}
