import { NextRequest } from "next/server";

export const dynamic = "force-dynamic";

async function proxy(req: NextRequest, { params }: { params: Promise<{ path: string[] }> }) {
  const { path } = await params;
  const backend = process.env.BACKEND_URL ?? "http://localhost:8080";
  const url = `${backend}/api/${path.join("/")}${req.nextUrl.search}`;
  try {
    const res = await fetch(url, {
      method: req.method,
      headers: { "Content-Type": "application/json" },
      body: req.method === "GET" || req.method === "HEAD" ? undefined : await req.text(),
      cache: "no-store",
    });
    return new Response(res.body, { status: res.status, headers: { "Content-Type": "application/json" } });
  } catch {
    return Response.json({ error: "The monitor backend is not reachable." }, { status: 502 });
  }
}

export { proxy as GET, proxy as PATCH };
