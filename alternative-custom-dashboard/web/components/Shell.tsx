"use client";
import Link from "next/link";
import { ReactNode } from "react";
import { ago } from "@/lib/api";

export type Crumb = { label: string; href?: string };

export function Shell({ crumbs, children, error, updatedAt }: {
  crumbs: Crumb[]; children: ReactNode; error?: string | null; updatedAt?: number | null;
}) {
  return (
    <>
      <header className="topbar">
        <nav aria-label="Breadcrumb" className="crumbs">
          <Link href="/" className="brand">Stream monitor</Link>
          {crumbs.map((c, i) => (
            <span key={i} className="crumb">
              <span aria-hidden className="sep">/</span>
              {c.href ? <Link href={c.href}>{c.label}</Link> : <span aria-current="page">{c.label}</span>}
            </span>
          ))}
        </nav>
        <span className="refreshed" aria-live="polite">
          {updatedAt ? `Updated ${ago((Date.now() - updatedAt) / 1000)}` : ""}
        </span>
      </header>
      {error && (
        <div className="banner" role="alert">
          {updatedAt ? "Can’t refresh right now, so this is the last data received. " : ""}{error}
        </div>
      )}
      <main className="page">{children}</main>
    </>
  );
}

export function Loading({ what }: { what: string }) {
  return <p className="muted">Loading {what}…</p>;
}
