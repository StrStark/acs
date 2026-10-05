import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, History, Link2, Plus, RotateCcw, Trash2, X } from "lucide-react";
import {
  deleteObject,
  objectInfo,
  objectUrl,
  objectVersions,
  restoreVersion,
  updateObject,
  type Bucket,
} from "../lib/api";
import { baseName, formatBytes, formatDate } from "../lib/format";
import { useCan } from "../lib/session";
import { Preview } from "./Preview";
import { Alert, Badge, Button, CopyButton, Field, IconButton, Spinner, useToast } from "./ui";

type Tab = "preview" | "details" | "versions";

/** Side panel showing one object: preview, metadata/tags editor and versions. */
export function ObjectPanel({
  bucket,
  objectKey,
  onClose,
  onShare,
  onDeleted,
}: {
  bucket: Bucket;
  objectKey: string;
  onClose: () => void;
  onShare: (key: string) => void;
  onDeleted: () => void;
}) {
  const can = useCan();
  const toast = useToast();
  const [tab, setTab] = useState<Tab>("preview");
  const info = useQuery({ queryKey: ["object", bucket.name, objectKey], queryFn: () => objectInfo(bucket.name, objectKey) });
  const versioned = bucket.versioning === "Enabled" || bucket.versioning === "Suspended";

  useEffect(() => setTab("preview"), [objectKey]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const del = useMutation({
    mutationFn: () => deleteObject(bucket.name, objectKey),
    onSuccess: () => {
      toast(versioned ? "Deleted (previous versions kept)" : "Deleted");
      onDeleted();
    },
    onError: (e) => toast(e.message, "error"),
  });

  const o = info.data;
  const name = baseName(objectKey);

  return (
    <aside className="fixed inset-y-0 right-0 z-30 flex w-full flex-col border-l border-zinc-200 bg-white shadow-2xl sm:w-[28rem] dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center gap-2 border-b border-zinc-200 px-4 py-3 dark:border-zinc-800">
        <h2 className="min-w-0 flex-1 truncate font-semibold" title={objectKey}>
          {name}
        </h2>
        <IconButton icon={X} label="Close" onClick={onClose} />
      </div>

      <div className="flex gap-1 border-b border-zinc-200 px-4 dark:border-zinc-800">
        {(["preview", "details", ...(versioned ? ["versions"] : [])] as Tab[]).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={`-mb-px border-b-2 px-2 py-2.5 text-sm font-medium capitalize transition ${
              tab === t
                ? "border-accent-600 text-accent-700 dark:text-accent-400"
                : "border-transparent text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100"
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      <div className="flex-1 overflow-y-auto p-4">
        {info.isPending ? (
          <div className="grid h-40 place-items-center">
            <Spinner />
          </div>
        ) : info.error ? (
          <Alert>{info.error.message}</Alert>
        ) : o && tab === "preview" ? (
          <div className="space-y-4">
            <Preview url={objectUrl(bucket.name, objectKey)} name={name} contentType={o.contentType} size={o.size} />
            <dl className="grid grid-cols-3 gap-y-2 text-sm">
              <dt className="text-zinc-500">Size</dt>
              <dd className="col-span-2 tabular-nums">{formatBytes(o.size)}</dd>
              <dt className="text-zinc-500">Type</dt>
              <dd className="col-span-2 truncate">{o.contentType}</dd>
              <dt className="text-zinc-500">Modified</dt>
              <dd className="col-span-2">{formatDate(o.lastModified)}</dd>
            </dl>
          </div>
        ) : o && tab === "details" ? (
          <Details bucket={bucket.name} objectKey={objectKey} info={o} editable={can.write} />
        ) : (
          <Versions bucket={bucket.name} objectKey={objectKey} canWrite={can.write} />
        )}
      </div>

      <div className="flex flex-wrap gap-2 border-t border-zinc-200 p-4 dark:border-zinc-800">
        <a href={objectUrl(bucket.name, objectKey, { download: true })} className="contents">
          <Button icon={Download}>Download</Button>
        </a>
        {can.write && (
          <Button variant="secondary" icon={Link2} onClick={() => onShare(objectKey)}>
            Share
          </Button>
        )}
        {can.write && (
          <Button
            variant="ghost"
            icon={Trash2}
            className="ml-auto text-red-600 dark:text-red-400"
            loading={del.isPending}
            onClick={() => {
              if (confirm(`Delete ${name}?`)) del.mutate();
            }}
          >
            Delete
          </Button>
        )}
      </div>
    </aside>
  );
}

type KV = { k: string; v: string };
const toList = (m?: Record<string, string>): KV[] => Object.entries(m ?? {}).map(([k, v]) => ({ k, v }));
const toMap = (l: KV[]) => Object.fromEntries(l.filter((x) => x.k.trim()).map((x) => [x.k.trim(), x.v]));

function Details({
  bucket,
  objectKey,
  info,
  editable,
}: {
  bucket: string;
  objectKey: string;
  info: NonNullable<Awaited<ReturnType<typeof objectInfo>>>;
  editable: boolean;
}) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const [contentType, setContentType] = useState(info.contentType ?? "");
  const [meta, setMeta] = useState<KV[]>(toList(info.metadata));
  const [tags, setTags] = useState<KV[]>(toList(info.tags));
  const save = useMutation({
    mutationFn: () => updateObject(bucket, objectKey, { contentType, metadata: toMap(meta), tags: toMap(tags) }),
    onSuccess: (o) => {
      queryClient.setQueryData(["object", bucket, objectKey], o);
      toast("Saved");
    },
    onError: (e) => toast(e.message, "error"),
  });

  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <span className="block text-sm font-medium text-zinc-700 dark:text-zinc-300">Key</span>
        <div className="flex items-center gap-1">
          <code className="min-w-0 flex-1 truncate rounded-md bg-zinc-100 px-2 py-1 text-xs dark:bg-zinc-800">{objectKey}</code>
          <CopyButton value={objectKey} />
        </div>
      </div>
      {info.etag && (
        <div className="space-y-1.5">
          <span className="block text-sm font-medium text-zinc-700 dark:text-zinc-300">ETag</span>
          <code className="block truncate rounded-md bg-zinc-100 px-2 py-1 text-xs dark:bg-zinc-800">{info.etag}</code>
        </div>
      )}
      <Field label="Content type" value={contentType} onChange={(e) => setContentType(e.target.value)} disabled={!editable} />
      <KVEditor title="Metadata" items={meta} onChange={setMeta} editable={editable} />
      <KVEditor title="Tags" items={tags} onChange={setTags} editable={editable} max={10} />
      {editable && (
        <Button loading={save.isPending} onClick={() => save.mutate()}>
          Save changes
        </Button>
      )}
    </div>
  );
}

function KVEditor({ title, items, onChange, editable, max }: { title: string; items: KV[]; onChange: (l: KV[]) => void; editable: boolean; max?: number }) {
  const set = (i: number, p: Partial<KV>) => onChange(items.map((x, j) => (j === i ? { ...x, ...p } : x)));
  const inp = "min-w-0 flex-1 rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm dark:border-zinc-700 dark:bg-zinc-900";
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium text-zinc-700 dark:text-zinc-300">{title}</span>
        {editable && (!max || items.length < max) && (
          <Button size="sm" variant="ghost" icon={Plus} onClick={() => onChange([...items, { k: "", v: "" }])}>
            Add
          </Button>
        )}
      </div>
      {items.length === 0 && <p className="text-xs text-zinc-500">None</p>}
      {items.map((x, i) => (
        <div key={i} className="flex items-center gap-2">
          <input className={inp} placeholder="key" value={x.k} disabled={!editable} onChange={(e) => set(i, { k: e.target.value })} />
          <input className={inp} placeholder="value" value={x.v} disabled={!editable} onChange={(e) => set(i, { v: e.target.value })} />
          {editable && <IconButton icon={X} label="Remove" onClick={() => onChange(items.filter((_, j) => j !== i))} />}
        </div>
      ))}
    </div>
  );
}

function Versions({ bucket, objectKey, canWrite }: { bucket: string; objectKey: string; canWrite: boolean }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const versions = useQuery({ queryKey: ["versions", bucket, objectKey], queryFn: () => objectVersions(bucket, objectKey) });
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["versions", bucket, objectKey] });
    queryClient.invalidateQueries({ queryKey: ["object", bucket, objectKey] });
    queryClient.invalidateQueries({ queryKey: ["objects", bucket] });
  };
  const restore = useMutation({
    mutationFn: (vid: string) => restoreVersion(bucket, objectKey, vid),
    onSuccess: () => {
      toast("Version restored");
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });
  const remove = useMutation({
    mutationFn: (vid: string) => deleteObject(bucket, objectKey, vid),
    onSuccess: () => {
      toast("Version permanently deleted");
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  if (versions.isPending) return <Spinner />;
  if (versions.error) return <Alert>{versions.error.message}</Alert>;
  return (
    <ul className="space-y-2">
      {versions.data?.map((v) => (
        <li key={v.versionId} className="rounded-xl border border-zinc-200 p-3 dark:border-zinc-800">
          <div className="flex items-center gap-2">
            <History className="size-4 text-zinc-400" />
            <span className="text-sm font-medium">{formatDate(v.lastModified)}</span>
            {v.isLatest && <Badge tone="green">Current</Badge>}
            {v.deleteMarker && <Badge tone="red">Delete marker</Badge>}
          </div>
          <p className="mt-1 truncate font-mono text-xs text-zinc-500">
            {v.versionId} {!v.deleteMarker && `· ${formatBytes(v.size)}`}
          </p>
          <div className="mt-2 flex gap-1">
            {!v.deleteMarker && (
              <a href={objectUrl(bucket, objectKey, { download: true, versionId: v.versionId })} className="contents">
                <Button size="sm" variant="secondary" icon={Download}>
                  Download
                </Button>
              </a>
            )}
            {canWrite && !v.isLatest && !v.deleteMarker && (
              <Button size="sm" variant="secondary" icon={RotateCcw} onClick={() => restore.mutate(v.versionId!)}>
                Restore
              </Button>
            )}
            {canWrite && (
              <Button
                size="sm"
                variant="ghost"
                icon={Trash2}
                className="text-red-600 dark:text-red-400"
                onClick={() => confirm("Permanently delete this version?") && remove.mutate(v.versionId!)}
              >
                {v.deleteMarker ? "Undelete" : "Delete"}
              </Button>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}
