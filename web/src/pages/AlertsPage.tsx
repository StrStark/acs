import { useState } from "react";
import { Navigate, Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BellOff, CheckCheck, FileWarning, HardDrive, ScanSearch, ShieldAlert, Unplug, type LucideIcon } from "lucide-react";
import { alertSummary, listAlerts, resolveAlert, resolveAllAlerts, runIntegrityCheck, type AlertKind, type CheckReport, type StorageAlert } from "../lib/api";
import { formatDuration, plural, timeAgo } from "../lib/format";
import { useCan } from "../lib/session";
import { Alert, Badge, Button, Card, EmptyState, PageHeader, Spinner, useToast } from "../components/ui";

const kindInfo: Record<AlertKind, { icon: LucideIcon; label: string }> = {
  "object.missing": { icon: FileWarning, label: "Missing data" },
  "object.corrupt": { icon: ShieldAlert, label: "Corrupt data" },
  "backend.offline": { icon: Unplug, label: "Storage offline" },
  "replica.failed": { icon: HardDrive, label: "Metadata copy" },
  "storage.foreign": { icon: HardDrive, label: "Other installation" },
};

export function AlertsPage() {
  const can = useCan();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [state, setState] = useState<"open" | "resolved">("open");
  const list = useQuery({ queryKey: ["alerts", state], queryFn: () => listAlerts(state), enabled: can.admin, refetchInterval: 15_000 });
  const summary = useQuery({
    queryKey: ["alert-summary"],
    queryFn: alertSummary,
    enabled: can.admin,
    refetchInterval: (q) => (q.state.data?.checking ? 2_000 : 30_000),
  });
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["alerts"] });
    queryClient.invalidateQueries({ queryKey: ["alert-summary"] });
  };
  const resolve = useMutation({ mutationFn: resolveAlert, onSuccess: refresh, onError: (e) => toast(e.message, "error") });
  const resolveAll = useMutation({ mutationFn: resolveAllAlerts, onSuccess: refresh, onError: (e) => toast(e.message, "error") });
  const check = useMutation({
    mutationFn: runIntegrityCheck,
    onSuccess: (r) => {
      toast(r.started ? "Integrity check started" : "A check is already running");
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  if (!can.admin) return <Navigate to="/" replace />;
  const s = summary.data;

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title="Alerts"
        description="ACS checks that every object's data is still where its metadata says, on every drive and provider, and alerts you here and through webhooks."
        actions={
          <>
            <Button variant="secondary" icon={ScanSearch} loading={check.isPending && check.variables === false} disabled={s?.checking} onClick={() => check.mutate(false)}>
              Quick check
            </Button>
            <Button variant="secondary" loading={check.isPending && check.variables === true} disabled={s?.checking} onClick={() => check.mutate(true)}>
              Deep check
            </Button>
          </>
        }
      />
      {s && <CheckStatus checking={s.checking} last={s.lastCheck ?? undefined} />}

      <div className="mb-3 mt-6 flex items-center gap-2">
        {(["open", "resolved"] as const).map((st) => (
          <button
            key={st}
            type="button"
            onClick={() => setState(st)}
            className={`rounded-lg px-3 py-1.5 text-sm font-medium capitalize transition ${
              state === st ? "bg-zinc-900 text-white dark:bg-zinc-100 dark:text-zinc-900" : "text-zinc-600 hover:bg-zinc-100 dark:text-zinc-400 dark:hover:bg-zinc-800"
            }`}
          >
            {st}
            {st === "open" && s && s.open > 0 ? ` (${s.open})` : ""}
          </button>
        ))}
        {state === "open" && (list.data?.length ?? 0) > 1 && (
          <Button variant="ghost" icon={CheckCheck} className="ml-auto" loading={resolveAll.isPending} onClick={() => resolveAll.mutate()}>
            Resolve all
          </Button>
        )}
      </div>

      {list.error && <Alert>{list.error.message}</Alert>}
      {!list.data ? (
        <Spinner />
      ) : list.data.length === 0 ? (
        <EmptyState icon={BellOff} title={state === "open" ? "No open alerts" : "No resolved alerts"}>
          {state === "open" && "All objects were found where they should be."}
        </EmptyState>
      ) : (
        <div className="space-y-2">
          {list.data.map((a) => (
            <AlertRow key={a.id} alert={a} onResolve={state === "open" ? () => resolve.mutate(a.id) : undefined} />
          ))}
        </div>
      )}
    </div>
  );
}

function CheckStatus({ checking, last }: { checking: boolean; last?: CheckReport }) {
  if (checking) {
    return (
      <Card className="flex items-center gap-3 p-4 text-sm">
        <Spinner className="size-4" /> Checking every object's data…
      </Card>
    );
  }
  if (!last) return <Card className="p-4 text-sm text-zinc-500">No integrity check has run yet. Quick checks run every 6 hours, deep checks weekly.</Card>;
  const problems = last.missing + last.corrupt;
  return (
    <Card className="flex flex-wrap items-center gap-x-6 gap-y-1 p-4 text-sm">
      <span className="font-medium">
        Last {last.deep ? "deep" : "quick"} check {timeAgo(last.startedAt)}
      </span>
      <span className="text-zinc-500">
        {plural(last.objects, "object")} · {plural(last.blobs, "data file")} · took {formatDuration(Math.max(1, Math.round(last.took / 1e9)))}
      </span>
      {last.error ? (
        <Badge tone="red">Failed: {last.error}</Badge>
      ) : problems > 0 ? (
        <Badge tone="red">
          {last.missing} missing · {last.corrupt} corrupt
        </Badge>
      ) : (
        <Badge tone="green">All data present</Badge>
      )}
      {last.offline > 0 && <Badge tone="amber">{plural(last.offline, "store")} offline</Badge>}
    </Card>
  );
}

function AlertRow({ alert: a, onResolve }: { alert: StorageAlert; onResolve?: () => void }) {
  const info = kindInfo[a.kind] ?? { icon: ShieldAlert, label: a.kind };
  const Icon = info.icon;
  const tone = a.severity === "critical" ? "text-red-600 bg-red-50 dark:bg-red-500/10 dark:text-red-400" : "text-amber-600 bg-amber-50 dark:bg-amber-500/10 dark:text-amber-400";
  return (
    <Card className="flex items-start gap-3 p-4">
      <div className={`grid size-9 shrink-0 place-items-center rounded-lg ${a.resolvedAt ? "bg-zinc-100 text-zinc-400 dark:bg-zinc-800" : tone}`}>
        <Icon className="size-4" />
      </div>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-2 font-medium">
          {a.title} <Badge>{info.label}</Badge>
          {a.count > 1 && <Badge tone="amber">seen {a.count}×</Badge>}
        </p>
        {a.detail && <p className="mt-0.5 text-sm text-zinc-600 dark:text-zinc-400">{a.detail}</p>}
        <p className="mt-1 text-xs text-zinc-500">
          {a.bucket && (
            <>
              <Link className="font-mono hover:underline" to={`/buckets/${a.bucket}`}>
                {a.bucket}
                {a.key ? `/${a.key}` : ""}
              </Link>
              {" · "}
            </>
          )}
          first seen {timeAgo(a.firstSeen)}
          {a.lastSeen !== a.firstSeen && ` · last seen ${timeAgo(a.lastSeen)}`}
          {a.resolvedAt && ` · resolved ${timeAgo(a.resolvedAt)}`}
        </p>
      </div>
      {onResolve && (
        <Button variant="ghost" onClick={onResolve}>
          Resolve
        </Button>
      )}
    </Card>
  );
}
