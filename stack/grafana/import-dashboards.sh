#!/usr/bin/env bash
# Creates the "Streaming" folder and imports both dashboards into any Grafana.
#
# This is what was already done by hand on production (monitor.a4b.io). Use it for sandbox,
# or for any new Grafana later.
#
#   GRAFANA_URL=https://monitor-sbox.a4b.io GRAFANA_TOKEN=glsa_xxx ./import-dashboards.sh
#
# Auth, pick one:
#   GRAFANA_TOKEN=...                  service account token (Administration > Service accounts)
#   GRAFANA_USER=... GRAFANA_PASS=...  basic auth
#
# Optional:
#   DS_UID=...        Prometheus datasource uid (default: auto-detect)
#   FOLDER=Streaming  folder title
#
# Safe to re-run: never overwrites an existing dashboard, reuses the folder if present.
set -euo pipefail
cd "$(dirname "$0")/dashboards"

: "${GRAFANA_URL:?set GRAFANA_URL, e.g. https://monitor-sbox.a4b.io}"
GRAFANA_URL="${GRAFANA_URL%/}"
FOLDER="${FOLDER:-Streaming}"

if [[ -n "${GRAFANA_TOKEN:-}" ]]; then
  AUTH=(-H "Authorization: Bearer $GRAFANA_TOKEN")
elif [[ -n "${GRAFANA_USER:-}" && -n "${GRAFANA_PASS:-}" ]]; then
  AUTH=(-u "$GRAFANA_USER:$GRAFANA_PASS")
else
  echo "error: set GRAFANA_TOKEN, or GRAFANA_USER and GRAFANA_PASS" >&2
  exit 1
fi

api() { # api METHOD PATH [BODY]
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -sS -m 30 -X "$method" "${AUTH[@]}" -H "Content-Type: application/json" -d "$body" "$GRAFANA_URL$path"
  else
    curl -sS -m 30 -X "$method" "${AUTH[@]}" "$GRAFANA_URL$path"
  fi
}

echo "Grafana: $GRAFANA_URL"
whoami=$(api GET /api/user | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
if not isinstance(d,dict) or not d.get("login"): sys.exit(1)
print(d["login"])' 2>/dev/null || true)
[[ -z "$whoami" ]] && { echo "error: auth failed — check GRAFANA_TOKEN / GRAFANA_USER / GRAFANA_PASS" >&2; exit 1; }
echo "Authenticated as: $whoami"

# ---- Prometheus datasource ----
if [[ -z "${DS_UID:-}" ]]; then
  DS_UID=$(api GET /api/datasources | python3 -c '
import json,sys
ds=[d for d in json.load(sys.stdin) if d.get("type")=="prometheus"]
if not ds: sys.exit("no prometheus datasource found - pass DS_UID=...")
pick=next((d for d in ds if d.get("isDefault")), ds[0])
if len(ds)>1:
    print("  %d prometheus datasources; picked %s" % (len(ds), pick["name"]), file=sys.stderr)
print(pick["uid"])')
fi
DS_NAME=$(api GET "/api/datasources/uid/$DS_UID" | python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])')
echo "Datasource: $DS_NAME ($DS_UID)"

# ---- folder: reuse if it already exists ----
FOLDER_UID=$(api GET /api/folders | python3 -c "
import json,sys
for f in json.load(sys.stdin):
    if f['title']=='''$FOLDER''': print(f['uid']); break")

if [[ -n "$FOLDER_UID" ]]; then
  echo "Folder:     $FOLDER (existing, $FOLDER_UID)"
else
  FOLDER_UID=$(api POST /api/folders "$(python3 -c "import json;print(json.dumps({'title':'''$FOLDER'''}))")" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("uid",""))')
  [[ -z "$FOLDER_UID" ]] && { echo "error: could not create folder" >&2; exit 1; }
  echo "Folder:     $FOLDER (created, $FOLDER_UID)"
fi

# ---- dashboards ----
# overwrite:false alone is NOT enough: these JSONs pin "version": 1, so if the stored dashboard is
# also at version 1 Grafana's optimistic-concurrency check passes and it updates anyway. Check for
# the uid explicitly and skip, so a re-run can never clobber someone's edits.
for f in streaming-laptops.json streaming-laptop.json; do
  uid=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["uid"])' "$f")
  if api GET "/api/dashboards/uid/$uid" | grep -q '"uid"'; then
    echo "  skip     $uid (already exists — delete it first if you want a fresh import)"
    continue
  fi

  body=$(DS_UID="$DS_UID" DS_NAME="$DS_NAME" FOLDER_UID="$FOLDER_UID" python3 -c '
import json,os,sys
d=json.load(open(sys.argv[1]))
for v in d.get("templating",{}).get("list",[]):
    if v.get("name")=="datasource":
        v["current"]={"text":os.environ["DS_NAME"],"value":os.environ["DS_UID"],"selected":True}
d.pop("id",None)
json.dump({"dashboard":d,"folderUid":os.environ["FOLDER_UID"],
           "overwrite":False,"message":"Imported by import-dashboards.sh"},sys.stdout)' "$f")

  api POST /api/dashboards/db "$body" | python3 -c '
import json,sys
r=json.load(sys.stdin)
if r.get("status")=="success":
    print("  ok       %-14s %s" % (r["uid"], r["url"]))
else:
    print("  SKIPPED: %s" % r.get("message", r))'
done

echo
echo "Done. Open $GRAFANA_URL/d/stream-fleet"
echo "Both will show \"No data\" until the agent is pushing and the recording rules are loaded."
