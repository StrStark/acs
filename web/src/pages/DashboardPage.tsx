import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Clock, Database, FileStack, HardDrive, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { getSettings, getSystemInfo, listAudit, listBuckets } from "../lib/api";
import { formatBytes, formatDuration, formatNumber, plural, timeAgo } from "../lib/format";
import { useCan } from "../lib/session";
import { Alert, Card, CopyField, PageHeader } from "../components/ui";

export function DashboardPage() {
  const can = useCan();
  const sys = useQuery({ queryKey: ["system"], queryFn: getSystemInfo, refetchInterval: 30_000 });
  const buckets = useQuery({ queryKey: ["buckets"], queryFn: listBuckets });
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const audit = useQuery({ queryKey: ["audit", "recent"], queryFn: () => listAudit({ limit: 8 }), enabled: can.admin });

  const d = sys.data;
  const usedPct = d && d.disk.total > 0 ? Math.round((d.disk.used / d.disk.total) * 100) : 0;
  const top = [...(buckets.data ?? [])].sort((a, b) => b.stats.storedBytes - a.stats.storedBytes).slice(0, 5);

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader title="Dashboard" description="Overview of this storage server." />
      {sys.error && <Alert>Could not load system info: {sys.error.message}</Alert>}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat icon={Database} label="Buckets" loading={sys.isPending}>
          {d && <Big>{formatNumber(d.totals.buckets)}</Big>}
        </Stat>
        <Stat icon={FileStack} label="Objects" loading={sys.isPending}>
          {d && (
            <>
              <Big>{formatNumber(d.totals.objects)}</Big>
              <Sub>{formatBytes(d.totals.bytes)}</Sub>
            </>
          )}
        </Stat>
        <Stat icon={HardDrive} label="Disk" loading={sys.isPending}>
          {d && (
            <>
              <Big>{formatBytes(d.disk.used)}</Big>
              <Sub>of {formatBytes(d.disk.total)}</Sub>
              <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
                <div
                  className={`h-full rounded-full ${usedPct > 90 ? "bg-red-500" : usedPct > 75 ? "bg-amber-500" : "bg-accent-500"}`}
                  style={{ width: `${usedPct}%` }}
                />
              </div>
            </>
          )}
        </Stat>
        <Stat icon={Clock} label="Uptime" loading={sys.isPending}>
          {d && (
            <>
              <Big>{formatDuration(d.uptimeSeconds)}</Big>
              <Sub>{/^\d/.test(d.version) ? `v${d.version}` : d.version}</Sub>
            </>
          )}
        </Stat>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-3">
        <Card className="p-5 lg:col-span-2">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="font-semibold">Largest buckets</h2>
            <Link to="/buckets" className="inline-flex items-center gap-1 text-sm text-accent-600 hover:underline dark:text-accent-400">
              All buckets <ArrowRight className="size-3.5" />
            </Link>
          </div>
          {top.length === 0 ? (
            <p className="py-6 text-center text-sm text-zinc-500">
              No buckets yet.{" "}
              {can.manageBuckets && (
                <Link to="/buckets" className="text-accent-600 hover:underline dark:text-accent-400">
                  Create your first bucket
                </Link>
              )}
            </p>
          ) : (
            <ul className="space-y-3">
              {top.map((b) => {
                const max = top[0].stats.storedBytes || 1;
                return (
                  <li key={b.name}>
                    <div className="flex justify-between text-sm">
                      <Link to={`/buckets/${b.name}`} className="font-medium hover:underline">
                        {b.name}
                      </Link>
                      <span className="tabular-nums text-zinc-500">
                        {formatBytes(b.stats.storedBytes)} · {plural(b.stats.objects, "object")}
                      </span>
                    </div>
                    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
                      <div className="h-full rounded-full bg-accent-500" style={{ width: `${(b.stats.storedBytes / max) * 100}%` }} />
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </Card>

        <Card className="space-y-4 p-5">
          <h2 className="font-semibold">Connect</h2>
          {settings.data && (
            <>
              <CopyField label="S3 endpoint" value={settings.data.s3Endpoint} />
              <CopyField label="Region" value={settings.data.region} />
              <p className="text-xs text-zinc-500 dark:text-zinc-400">
                Use path-style addressing with any S3 client.{" "}
                <Link to="/keys" className="text-accent-600 hover:underline dark:text-accent-400">
                  Create an access key
                </Link>{" "}
                to get credentials.
              </p>
            </>
          )}
        </Card>
      </div>

      {can.admin && (
        <Card className="mt-6 p-5">
          <div className="mb-3 flex items-center justify-between">
            <h2 className="font-semibold">Recent activity</h2>
            <Link to="/audit" className="inline-flex items-center gap-1 text-sm text-accent-600 hover:underline dark:text-accent-400">
              Audit log <ArrowRight className="size-3.5" />
            </Link>
          </div>
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {(audit.data ?? []).map((e) => (
              <li key={e.id} className="flex items-center justify-between gap-4 py-2 text-sm">
                <span className="min-w-0 truncate">
                  <span className="font-medium">{e.actor}</span> <span className="text-zinc-500">{e.action}</span>{" "}
                  <span className="font-mono text-xs">{e.target}</span>
                </span>
                <span className="shrink-0 text-xs text-zinc-500">{timeAgo(e.time)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}

const Big = ({ children }: { children: ReactNode }) => <span className="text-2xl font-semibold tabular-nums">{children}</span>;
const Sub = ({ children }: { children: ReactNode }) => <span className="ml-1.5 text-sm text-zinc-500">{children}</span>;

function Stat({ icon: Icon, label, loading, children }: { icon: LucideIcon; label: string; loading: boolean; children: ReactNode }) {
  return (
    <Card className="p-5">
      <div className="flex items-center gap-2 text-sm text-zinc-500 dark:text-zinc-400">
        <Icon className="size-4" />
        {label}
      </div>
      <div className="mt-3">{loading ? <div className="h-8 w-24 animate-pulse rounded-md bg-zinc-100 dark:bg-zinc-800" /> : children}</div>
    </Card>
  );
}
