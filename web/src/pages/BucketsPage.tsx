import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cloud, Database, Globe, HardDrive, History, Link2, Plus } from "lucide-react";
import { ApiError, createBucket, listBuckets, listStorage } from "../lib/api";
import { providerLabel } from "./StoragePage";
import { formatBytes, formatDate, formatNumber } from "../lib/format";
import { useCan } from "../lib/session";
import { Alert, Badge, Button, EmptyState, Field, Modal, PageHeader, Select, Spinner, Table, Toggle, td, th } from "../components/ui";

export function BucketsPage() {
  const can = useCan();
  const [creating, setCreating] = useState(false);
  const { data, isPending, error } = useQuery({ queryKey: ["buckets"], queryFn: listBuckets });
  const storage = useQuery({ queryKey: ["storage"], queryFn: listStorage, enabled: can.admin });
  const storageNames = Object.fromEntries((storage.data?.backends ?? []).map((b) => [b.id, b]));

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Buckets"
        description="Top-level containers for your objects."
        actions={
          can.manageBuckets && (
            <Button icon={Plus} onClick={() => setCreating(true)}>
              New bucket
            </Button>
          )
        }
      />
      {error && <Alert>{error.message}</Alert>}
      {isPending ? (
        <div className="grid place-items-center py-20">
          <Spinner />
        </div>
      ) : data && data.length === 0 ? (
        <EmptyState
          icon={Database}
          title="No buckets yet"
          action={can.manageBuckets && <Button icon={Plus} onClick={() => setCreating(true)}>Create bucket</Button>}
        >
          Buckets hold your files. Create one, then upload from the browser or any S3 client.
        </EmptyState>
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>Name</th>
              <th className={th}>Objects</th>
              <th className={th}>Size</th>
              <th className={`${th} hidden md:table-cell`}>Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {data?.map((b) => (
              <tr key={b.name} className="hover:bg-zinc-50 dark:hover:bg-zinc-800/40">
                <td className={td}>
                  <Link to={`/buckets/${b.name}`} className="flex items-center gap-2.5 font-medium hover:underline">
                    <Database className="size-4 text-accent-600" />
                    {b.name}
                  </Link>
                  <div className="mt-1 flex gap-1.5 pl-6.5">
                    {b.versioning === "Enabled" && (
                      <Badge tone="accent">
                        <History className="mr-1 size-3" />
                        Versioned
                      </Badge>
                    )}
                    {b.public && (
                      <Badge tone="amber">
                        <Globe className="mr-1 size-3" />
                        Public
                      </Badge>
                    )}
                    {b.backend && (
                      <Badge>
                        {storageNames[b.backend]?.type === "local" ? <HardDrive className="mr-1 size-3" /> : <Cloud className="mr-1 size-3" />}
                        {b.mode === "mirror" ? "Mirror · " : ""}
                        {storageNames[b.backend]?.name ?? "External storage"}
                      </Badge>
                    )}
                  </div>
                </td>
                <td className={`${td} tabular-nums`}>{b.mode === "mirror" ? "—" : formatNumber(b.stats.objects)}</td>
                <td className={`${td} tabular-nums`}>
                  {b.mode === "mirror" ? (
                    "—"
                  ) : (
                    <>
                      {formatBytes(b.stats.storedBytes)}
                      {b.quotaBytes ? <span className="text-zinc-500"> / {formatBytes(b.quotaBytes)}</span> : null}
                    </>
                  )}
                </td>
                <td className={`${td} hidden text-zinc-500 md:table-cell`}>{formatDate(b.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <CreateBucketDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  );
}

function CreateBucketDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const can = useCan();
  const [name, setName] = useState("");
  const [versioning, setVersioning] = useState(false);
  const [backend, setBackend] = useState("");
  const [mode, setMode] = useState<"" | "mirror">("");
  const [remotePrefix, setRemotePrefix] = useState("");
  const storage = useQuery({ queryKey: ["storage"], queryFn: listStorage, enabled: open && can.admin });
  const selected = storage.data?.backends.find((b) => b.id === backend);
  const isS3 = selected?.type === "s3";
  const mirror = isS3 && mode === "mirror";

  const mutation = useMutation({
    mutationFn: createBucket,
    onSuccess: (b) => {
      queryClient.invalidateQueries({ queryKey: ["buckets"] });
      queryClient.invalidateQueries({ queryKey: ["storage"] });
      onClose();
      setName("");
      navigate(`/buckets/${b.name}`);
    },
  });
  const fields = mutation.error instanceof ApiError ? mutation.error.fields : {};

  function submit(e: FormEvent) {
    e.preventDefault();
    mutation.mutate({
      name: name.trim(),
      versioning: versioning && !mirror,
      backend: backend || undefined,
      mode: mirror ? "mirror" : undefined,
      remotePrefix: mirror ? remotePrefix : undefined,
    });
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New bucket"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="create-bucket" loading={mutation.isPending}>
            Create bucket
          </Button>
        </>
      }
    >
      <form id="create-bucket" onSubmit={submit} className="space-y-5">
        {mutation.error && !fields.name && <Alert>{mutation.error.message}</Alert>}
        <Field
          label="Bucket name"
          name="name"
          value={name}
          onChange={(e) => setName(e.target.value.toLowerCase())}
          error={fields.name}
          hint="3–63 characters: lowercase letters, numbers, dots and hyphens."
          placeholder="my-bucket"
          autoFocus
          required
        />
        {can.admin && storage.data && storage.data.backends.length > 0 && (
          <Select
            label="Storage"
            value={backend}
            onChange={(e) => {
              setBackend(e.target.value);
              setMode("");
            }}
          >
            <option value="">{storage.data.builtin.name}</option>
            {storage.data.backends.map((b) => (
              <option key={b.id} value={b.id} disabled={!b.status.online}>
                {b.name} ({b.type === "local" ? "drive" : providerLabel(b.provider)}){b.status.online ? "" : " — offline"}
              </option>
            ))}
          </Select>
        )}
        {isS3 && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-2">
              {(
                [
                  ["", Database, "Store data", "ACS manages the files. All features: versioning, quotas, lifecycle."],
                  ["mirror", Link2, "Mirror remote", "Live view of files at the provider, with real names. Existing files appear."],
                ] as const
              ).map(([m, Icon, title, desc]) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => setMode(m)}
                  className={`rounded-xl border p-3 text-left transition ${
                    mode === m ? "border-accent-500 bg-accent-50 dark:bg-accent-500/10" : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700"
                  }`}
                >
                  <Icon className="mb-2 size-4 text-accent-600" />
                  <span className="block text-sm font-medium">{title}</span>
                  <span className="block text-xs text-zinc-500">{desc}</span>
                </button>
              ))}
            </div>
            {mirror && (
              <Field
                label="Folder at the provider (optional)"
                value={remotePrefix}
                onChange={(e) => setRemotePrefix(e.target.value)}
                placeholder="photos/"
                hint={`Mirrors ${selected!.bucket}/${selected!.prefix ?? ""}${remotePrefix.replace(/^\/+/, "")}`}
              />
            )}
          </div>
        )}
        {!mirror && (
          <Toggle
            checked={versioning}
            onChange={setVersioning}
            label="Versioning"
            description="Keep every version of each object so overwrites and deletes can be undone."
          />
        )}
      </form>
    </Modal>
  );
}
