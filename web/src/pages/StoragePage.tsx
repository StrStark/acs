import { useState, type FormEvent } from "react";
import { Navigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cloud, HardDrive, Pencil, Plus, RefreshCw, Server, Trash2 } from "lucide-react";
import {
  ApiError,
  checkStorage,
  createStorage,
  deleteStorage,
  listStorage,
  testStorage,
  updateStorage,
  type BackendInput,
  type BackendStatus,
  type StorageBackend,
} from "../lib/api";
import { formatBytes, plural, timeAgo } from "../lib/format";
import { useCan } from "../lib/session";
import { Menu } from "../components/Menu";
import { Alert, Badge, Button, Card, ConfirmDialog, Field, Modal, PageHeader, Select, Spinner, Toggle, useToast } from "../components/ui";

/** Provider presets fill in endpoints and quirks for common S3 services. */
export const providers = [
  { id: "arvan-simin", label: "ArvanCloud — Simin (Tehran)", endpoint: "https://s3.ir-thr-at1.arvanstorage.ir", pathStyle: true, hint: "Create a bucket in the ArvanCloud panel, then copy the access and secret keys from Object Storage → Access keys." },
  { id: "arvan-shahriar", label: "ArvanCloud — Shahriar (Tabriz)", endpoint: "https://s3.ir-tbz-sh1.arvanstorage.ir", pathStyle: true, hint: "Create a bucket in the ArvanCloud panel, then copy the access and secret keys from Object Storage → Access keys." },
  { id: "parspack", label: "ParsPack Cloud Storage", endpoint: "", placeholder: "https://c123456.parspack.net", pathStyle: true, hint: "Use the endpoint, bucket and keys shown in your ParsPack cloud-storage panel. ParsPack rate-limits bursts; ACS retries automatically." },
  { id: "aws", label: "Amazon S3", endpoint: "https://s3.us-east-1.amazonaws.com", pathStyle: false, region: "us-east-1", hint: "Use the regional endpoint (https://s3.<region>.amazonaws.com) and set the same region." },
  { id: "minio", label: "MinIO / self-hosted", endpoint: "", placeholder: "https://minio.example.com:9000", pathStyle: true },
  { id: "other", label: "Other S3-compatible", endpoint: "", placeholder: "https://s3.example.com", pathStyle: true },
] as const;

export const providerLabel = (id?: string) => providers.find((p) => p.id === id)?.label ?? "S3-compatible";

export function StatusBadge({ status }: { status: BackendStatus }) {
  return status.online ? <Badge tone="green">Online</Badge> : <Badge tone="red">Offline</Badge>;
}

export function StoragePage() {
  const can = useCan();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<StorageBackend | null>(null);
  const [deleting, setDeleting] = useState<StorageBackend | null>(null);
  const list = useQuery({ queryKey: ["storage"], queryFn: listStorage, enabled: can.admin });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["storage"] });

  const check = useMutation({
    mutationFn: (b: StorageBackend) => checkStorage(b.id),
    onSuccess: (b) => {
      toast(b.status.online ? `${b.name} is online` : `${b.name}: ${b.status.error}`, b.status.online ? "success" : "error");
      refresh();
    },
  });
  const del = useMutation({
    mutationFn: (b: StorageBackend) => deleteStorage(b.id),
    onSuccess: () => {
      toast("Storage backend removed");
      setDeleting(null);
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  if (!can.admin) return <Navigate to="/" replace />;
  const d = list.data;

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title="Storage"
        description="Where bucket data lives: the built-in disk, attached drives, or S3-compatible providers like ArvanCloud and ParsPack."
        actions={<Button icon={Plus} onClick={() => setAdding(true)}>Add storage</Button>}
      />
      {list.error && <Alert>{list.error.message}</Alert>}
      {!d ? (
        <Spinner />
      ) : (
        <div className="space-y-3">
          <BackendCard
            icon={Server}
            name={d.builtin.name}
            detail={d.builtin.path ?? "Data volume"}
            kind="Built-in"
            status={d.builtin.status}
            buckets={d.builtinBuckets}
            stored={d.builtinStoredBytes}
          />
          {d.backends.map((b) => (
            <BackendCard
              key={b.id}
              icon={b.type === "local" ? HardDrive : Cloud}
              name={b.name}
              detail={b.type === "local" ? b.path! : `${b.endpoint}/${b.bucket}${b.prefix ? `/${b.prefix}` : ""}`}
              kind={b.type === "local" ? "Drive" : providerLabel(b.provider)}
              status={b.status}
              buckets={b.buckets}
              stored={b.storedBytes}
              actions={
                <Menu
                  items={[
                    { label: "Check now", icon: RefreshCw, onClick: () => check.mutate(b) },
                    { label: "Edit", icon: Pencil, onClick: () => setEditing(b) },
                    { label: "Remove", icon: Trash2, danger: true, onClick: () => setDeleting(b) },
                  ]}
                />
              }
            />
          ))}
          {d.backends.length === 0 && (
            <Card className="p-6 text-sm text-zinc-500 dark:text-zinc-400">
              <p className="font-medium text-zinc-900 dark:text-zinc-100">Add more storage</p>
              <ul className="mt-2 list-disc space-y-1 pl-5">
                <li>
                  <b>Attached drive</b>: mount a disk into the container (e.g. <code>- /mnt/disk2:/drives/disk2</code> in docker-compose.yml) and add its path.
                </li>
                <li>
                  <b>S3 provider</b>: store bucket data on ArvanCloud, ParsPack, AWS or MinIO, or mirror an existing remote bucket with its real file names.
                </li>
              </ul>
            </Card>
          )}
        </div>
      )}
      {adding && <BackendDialog onClose={() => setAdding(false)} onDone={refresh} />}
      {editing && <BackendDialog backend={editing} onClose={() => setEditing(null)} onDone={refresh} />}
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting)}
        loading={del.isPending}
        title={`Remove ${deleting?.name}?`}
        confirmLabel="Remove"
      >
        ACS stops using this storage. Data already stored there is not deleted. A backend that buckets still use cannot be removed.
      </ConfirmDialog>
    </div>
  );
}

function BackendCard({
  icon: Icon,
  name,
  detail,
  kind,
  status,
  buckets,
  stored,
  actions,
}: {
  icon: typeof Server;
  name: string;
  detail: string;
  kind: string;
  status: BackendStatus;
  buckets: number;
  stored: number;
  actions?: React.ReactNode;
}) {
  const pct = status.totalBytes ? Math.round(((status.totalBytes - (status.freeBytes ?? 0)) / status.totalBytes) * 100) : null;
  return (
    <Card className="flex flex-wrap items-center gap-4 p-4">
      <div className="grid size-10 shrink-0 place-items-center rounded-xl bg-accent-50 text-accent-600 dark:bg-accent-500/10 dark:text-accent-400">
        <Icon className="size-5" />
      </div>
      <div className="min-w-0 flex-1">
        <p className="flex flex-wrap items-center gap-2 font-medium">
          {name} <Badge>{kind}</Badge> <StatusBadge status={status} />
        </p>
        <p className="truncate font-mono text-xs text-zinc-500" title={detail}>
          {detail}
        </p>
        {!status.online && status.error && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{status.error}</p>}
      </div>
      <div className="w-48 text-right text-sm">
        <p className="tabular-nums">
          {plural(buckets, "bucket")} · {formatBytes(stored)}
        </p>
        {pct !== null ? (
          <>
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
              <div className={`h-full rounded-full ${pct > 90 ? "bg-red-500" : "bg-accent-500"}`} style={{ width: `${pct}%` }} />
            </div>
            <p className="mt-1 text-xs text-zinc-500">{formatBytes(status.freeBytes ?? 0)} free</p>
          </>
        ) : (
          status.checkedAt && <p className="text-xs text-zinc-500">checked {timeAgo(status.checkedAt)}</p>
        )}
      </div>
      {actions}
    </Card>
  );
}

function BackendDialog({ backend, onClose, onDone }: { backend?: StorageBackend; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const editing = !!backend;
  const [form, setForm] = useState<BackendInput>(
    backend
      ? { ...backend, secretKey: "", prefix: backend.prefix?.replace(/\/$/, "") }
      : { name: "", type: "s3", provider: "arvan-simin", endpoint: providers[0].endpoint, pathStyle: true, region: "", bucket: "", prefix: "acs", accessKey: "", secretKey: "" },
  );
  const [testResult, setTestResult] = useState<{ ok: boolean; error?: string }>();
  const set = (p: Partial<BackendInput>) => {
    setForm((f) => ({ ...f, ...p }));
    setTestResult(undefined);
  };
  const preset = providers.find((p) => p.id === form.provider) ?? providers[providers.length - 1];

  const save = useMutation({
    mutationFn: () => (editing ? updateStorage(backend!.id, form) : createStorage(form)),
    onSuccess: (b) => {
      toast(editing ? "Storage updated" : `Added ${b.name}`);
      onClose();
      onDone();
    },
  });
  const test = useMutation({ mutationFn: () => testStorage(form), onSuccess: setTestResult });
  const fields = (save.error ?? test.error) instanceof ApiError ? ((save.error ?? test.error) as ApiError).fields : {};
  const generalError = (save.error ?? test.error) && Object.keys(fields).length === 0 ? (save.error ?? test.error)!.message : undefined;

  return (
    <Modal
      open
      wide
      onClose={onClose}
      title={editing ? `Edit ${backend!.name}` : "Add storage"}
      footer={
        <>
          {!(editing && form.type === "local") && (
            <Button variant="secondary" loading={test.isPending} onClick={() => test.mutate()} className="mr-auto">
              Test connection
            </Button>
          )}
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="backend-form" loading={save.isPending}>
            {editing ? "Save" : "Add storage"}
          </Button>
        </>
      }
    >
      <form
        id="backend-form"
        className="space-y-4"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        {generalError && <Alert>{generalError}</Alert>}
        {testResult && (testResult.ok ? <Alert tone="info">Connection works: ACS could write, read and delete a test object.</Alert> : <Alert>{testResult.error}</Alert>)}

        {!editing && (
          <div className="grid grid-cols-2 gap-2">
            {(
              [
                ["s3", Cloud, "S3 provider", "ArvanCloud, ParsPack, AWS, MinIO…"],
                ["local", HardDrive, "Attached drive", "A directory on a mounted disk"],
              ] as const
            ).map(([t, Icon, title, desc]) => (
              <button
                key={t}
                type="button"
                onClick={() => set({ type: t })}
                className={`rounded-xl border p-3 text-left transition ${
                  form.type === t ? "border-accent-500 bg-accent-50 dark:bg-accent-500/10" : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700"
                }`}
              >
                <Icon className="mb-2 size-4 text-accent-600" />
                <span className="block text-sm font-medium">{title}</span>
                <span className="block text-xs text-zinc-500">{desc}</span>
              </button>
            ))}
          </div>
        )}

        <Field label="Name" value={form.name} onChange={(e) => set({ name: e.target.value })} error={fields.name} placeholder={form.type === "local" ? "Disk 2" : "ArvanCloud Tehran"} required />

        {form.type === "local" ? (
          <Field
            label="Path inside the container"
            value={form.path ?? ""}
            onChange={(e) => set({ path: e.target.value })}
            error={fields.path}
            placeholder="/drives/disk2"
            disabled={editing}
            hint={
              <>
                Mount the drive first, e.g. <code>- /mnt/disk2:/drives/disk2</code> under <code>volumes</code> in docker-compose.yml, and make it writable by
                the container user (<code>chown 65532:65532 /mnt/disk2</code>).
              </>
            }
            required
          />
        ) : (
          <>
            {!editing && (
              <Select
                label="Provider"
                value={form.provider}
                onChange={(e) => {
                  const p = providers.find((x) => x.id === e.target.value)!;
                  set({ provider: p.id, endpoint: p.endpoint, pathStyle: p.pathStyle, region: "region" in p ? p.region : "" });
                }}
                hint={"hint" in preset ? preset.hint : undefined}
              >
                {providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.label}
                  </option>
                ))}
              </Select>
            )}
            <Field
              label="Endpoint"
              value={form.endpoint ?? ""}
              onChange={(e) => set({ endpoint: e.target.value })}
              error={fields.endpoint}
              placeholder={"placeholder" in preset ? preset.placeholder : "https://s3.example.com"}
              required
            />
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="Bucket at the provider"
                value={form.bucket ?? ""}
                onChange={(e) => set({ bucket: e.target.value })}
                error={fields.bucket}
                disabled={editing}
                hint="Must already exist."
                required
              />
              <Field
                label="Prefix (optional)"
                value={form.prefix ?? ""}
                onChange={(e) => set({ prefix: e.target.value })}
                disabled={editing}
                placeholder="acs"
                hint="Keeps ACS data in a sub-folder of the bucket."
              />
              <Field label="Access key" value={form.accessKey ?? ""} onChange={(e) => set({ accessKey: e.target.value })} error={fields.accessKey} autoComplete="off" required />
              <Field
                label="Secret key"
                type="password"
                value={form.secretKey ?? ""}
                onChange={(e) => set({ secretKey: e.target.value })}
                error={fields.secretKey}
                autoComplete="new-password"
                placeholder={editing ? "unchanged" : ""}
                required={!editing}
              />
              <Field label="Region (optional)" value={form.region ?? ""} onChange={(e) => set({ region: e.target.value })} placeholder="us-east-1" />
            </div>
            <Toggle checked={!!form.pathStyle} onChange={(v) => set({ pathStyle: v })} label="Path-style requests" description="Required by most providers other than AWS (ArvanCloud, ParsPack, MinIO)." />
            <Toggle checked={!!form.insecureTls} onChange={(v) => set({ insecureTls: v })} label="Skip TLS certificate verification" description="Only for self-signed test setups." />
          </>
        )}
      </form>
    </Modal>
  );
}
