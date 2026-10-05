import { useEffect, useMemo, useRef, useState, type DragEvent, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRightLeft,
  ChevronRight,
  Download,
  FileArchive,
  FolderPlus,
  FolderUp,
  HardDrive,
  Link2,
  Pencil,
  RefreshCw,
  Search,
  Trash2,
  Upload,
  UploadCloud,
} from "lucide-react";
import {
  bulkDelete,
  copyObject,
  createFolder,
  listObjects,
  objectUrl,
  zipUrl,
  type Bucket,
  type ObjectInfo,
} from "../lib/api";
import { baseName, formatBytes, formatDate, timeAgo } from "../lib/format";
import { filesFromDrop } from "../lib/upload";
import { useCan } from "../lib/session";
import { FileTypeIcon } from "./Preview";
import { Menu } from "./Menu";
import { ObjectPanel } from "./ObjectPanel";
import { ShareDialog } from "./ShareDialog";
import { useUploads } from "./Uploads";
import { Alert, Button, ConfirmDialog, EmptyState, Field, IconButton, Modal, Spinner, useToast } from "./ui";

type Row = { kind: "folder"; key: string } | { kind: "file"; key: string; obj: ObjectInfo };

export function FileBrowser({ bucket, prefix }: { bucket: Bucket; prefix: string }) {
  const can = useCan();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { enqueue } = useUploads();
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);

  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<string | null>(null);
  const [shareTarget, setShareTarget] = useState<string | null>(null);
  const [newFolder, setNewFolder] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [moving, setMoving] = useState<string[] | null>(null);
  const [deleting, setDeleting] = useState<string[] | null>(null);
  const [dragging, setDragging] = useState(false);

  const listing = useInfiniteQuery({
    queryKey: ["objects", bucket.name, prefix],
    queryFn: ({ pageParam }) => listObjects(bucket.name, { prefix, delimiter: "/", cursor: pageParam, limit: 500 }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });

  const rows: Row[] = useMemo(() => {
    const pages = listing.data?.pages ?? [];
    const out: Row[] = [];
    for (const p of pages) for (const key of p.prefixes) out.push({ kind: "folder", key });
    for (const p of pages)
      for (const obj of p.objects) if (obj.key !== prefix) out.push({ kind: "file", key: obj.key, obj });
    const f = filter.trim().toLowerCase();
    return f ? out.filter((r) => baseName(r.key).toLowerCase().includes(f)) : out;
  }, [listing.data, prefix, filter]);

  const refresh = () => {
    setSelected(new Set());
    queryClient.invalidateQueries({ queryKey: ["objects", bucket.name] });
    queryClient.invalidateQueries({ queryKey: ["buckets"] });
    queryClient.invalidateQueries({ queryKey: ["bucket", bucket.name] });
  };

  const uploadFiles = (files: { file: File; path: string }[]) => {
    if (files.length === 0) return;
    enqueue(bucket.name, files.map(({ file, path }) => ({ file, key: prefix + path })));
  };

  const onDrop = async (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    if (!can.write) return;
    uploadFiles(await filesFromDrop(e.dataTransfer));
  };

  const del = useMutation({
    mutationFn: (keys: string[]) =>
      bulkDelete(bucket.name, { keys: keys.filter((k) => !k.endsWith("/")), prefixes: keys.filter((k) => k.endsWith("/")) }),
    onSuccess: (r) => {
      toast(r.errors.length ? `Deleted ${r.deleted}, ${r.errors.length} failed` : `Deleted ${r.deleted} item${r.deleted === 1 ? "" : "s"}`,
        r.errors.length ? "error" : "success");
      setDeleting(null);
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  const allSelected = rows.length > 0 && rows.every((r) => selected.has(r.key));
  const toggle = (key: string) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(key)) n.delete(key);
      else n.add(key);
      return n;
    });

  const crumbs = prefix.split("/").filter(Boolean);
  const selectedKeys = [...selected];

  return (
    <div
      onDragOver={(e) => {
        if (can.write && e.dataTransfer.types.includes("Files")) {
          e.preventDefault();
          setDragging(true);
        }
      }}
      onDragLeave={(e) => e.currentTarget === e.target && setDragging(false)}
      onDrop={onDrop}
      className="relative"
    >
      {/* Toolbar */}
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <nav className="flex min-w-0 flex-1 items-center gap-1 text-sm" aria-label="Folder path">
          <Link to={`/buckets/${bucket.name}`} className="inline-flex items-center gap-1.5 rounded px-1.5 py-1 font-medium hover:bg-zinc-100 dark:hover:bg-zinc-800">
            <HardDrive className="size-4 text-zinc-400" />
            {bucket.name}
          </Link>
          {crumbs.map((c, i) => (
            <span key={i} className="flex min-w-0 items-center gap-1">
              <ChevronRight className="size-3.5 shrink-0 text-zinc-400" />
              <Link
                to={`/buckets/${bucket.name}/${crumbs.slice(0, i + 1).map(encodeURIComponent).join("/")}/`}
                className="truncate rounded px-1.5 py-1 hover:bg-zinc-100 dark:hover:bg-zinc-800"
              >
                {c}
              </Link>
            </span>
          ))}
        </nav>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-zinc-400" />
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter this folder"
            aria-label="Filter this folder"
            className="w-44 rounded-lg border border-zinc-300 bg-white py-1.5 pl-8 pr-3 text-sm outline-none focus:border-accent-500 focus:ring-4 focus:ring-accent-500/15 dark:border-zinc-700 dark:bg-zinc-900"
          />
        </div>
        <IconButton icon={RefreshCw} label="Refresh" onClick={refresh} />
        {can.write && (
          <>
            <Button variant="secondary" icon={FolderPlus} onClick={() => setNewFolder(true)}>
              New folder
            </Button>
            <Button variant="secondary" icon={FolderUp} onClick={() => folderInput.current?.click()}>
              Upload folder
            </Button>
            <Button icon={Upload} onClick={() => fileInput.current?.click()}>
              Upload
            </Button>
          </>
        )}
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            uploadFiles(Array.from(e.target.files ?? []).map((file) => ({ file, path: file.name })));
            e.target.value = "";
          }}
        />
        <input
          ref={folderInput}
          type="file"
          hidden
          // @ts-expect-error non-standard but widely supported directory picker
          webkitdirectory=""
          onChange={(e) => {
            uploadFiles(Array.from(e.target.files ?? []).map((file) => ({ file, path: file.webkitRelativePath || file.name })));
            e.target.value = "";
          }}
        />
      </div>

      {/* Selection bar */}
      {selected.size > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 rounded-xl bg-accent-50 px-3 py-2 text-sm dark:bg-accent-500/10">
          <span className="font-medium">{selected.size} selected</span>
          <a href={zipUrl(bucket.name, { prefix, keys: selectedKeys })} className="contents">
            <Button size="sm" variant="secondary" icon={FileArchive}>
              Download ZIP
            </Button>
          </a>
          {can.write && (
            <>
              <Button size="sm" variant="secondary" icon={ArrowRightLeft} onClick={() => setMoving(selectedKeys)}>
                Move
              </Button>
              <Button size="sm" variant="danger" icon={Trash2} onClick={() => setDeleting(selectedKeys)}>
                Delete
              </Button>
            </>
          )}
          <button type="button" className="ml-auto text-xs text-zinc-500 hover:underline" onClick={() => setSelected(new Set())}>
            Clear selection
          </button>
        </div>
      )}

      {listing.error && <Alert>{listing.error.message}</Alert>}

      {listing.isPending ? (
        <div className="grid place-items-center py-20">
          <Spinner />
        </div>
      ) : rows.length === 0 ? (
        <EmptyState
          icon={UploadCloud}
          title={filter ? "No matching items" : "This folder is empty"}
          action={can.write && !filter && <Button icon={Upload} onClick={() => fileInput.current?.click()}>Upload files</Button>}
        >
          {!filter && can.write && "Drag and drop files or folders here, or use the Upload button."}
        </EmptyState>
      ) : (
        <div className="overflow-hidden rounded-2xl border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900/60">
          <table className="w-full table-fixed text-left text-sm">
            <thead className="border-b border-zinc-200 text-xs uppercase tracking-wide text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
              <tr>
                <th className="w-10 px-4 py-2.5">
                  <input
                    type="checkbox"
                    aria-label="Select all"
                    checked={allSelected}
                    onChange={() => setSelected(allSelected ? new Set() : new Set(rows.map((r) => r.key)))}
                    className="accent-accent-600"
                  />
                </th>
                <th className="px-2 py-2.5 font-medium">Name</th>
                <th className="hidden w-28 px-4 py-2.5 font-medium sm:table-cell">Size</th>
                <th className="hidden w-44 px-4 py-2.5 font-medium md:table-cell">Modified</th>
                <th className="w-12" />
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
              {rows.map((r) => {
                const name = baseName(r.key);
                const isFolder = r.kind === "folder";
                return (
                  <tr
                    key={r.key}
                    className={`group cursor-pointer ${
                      selected.has(r.key) ? "bg-accent-50/60 dark:bg-accent-500/5" : "hover:bg-zinc-50 dark:hover:bg-zinc-800/40"
                    }`}
                    onClick={() =>
                      isFolder ? navigate(`/buckets/${bucket.name}/${r.key.split("/").map(encodeURIComponent).join("/")}`) : setOpen(r.key)
                    }
                  >
                    <td className="px-4 py-2.5" onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        aria-label={`Select ${name}`}
                        checked={selected.has(r.key)}
                        onChange={() => toggle(r.key)}
                        className="accent-accent-600"
                      />
                    </td>
                    <td className="px-2 py-2.5">
                      <div className="flex min-w-0 items-center gap-2.5">
                        <FileTypeIcon name={name} folder={isFolder} contentType={isFolder ? undefined : r.obj.contentType} />
                        <span className="truncate font-medium" title={r.key}>
                          {name}
                        </span>
                      </div>
                    </td>
                    <td className="hidden px-4 py-2.5 tabular-nums text-zinc-500 sm:table-cell">{isFolder ? "—" : formatBytes(r.obj.size)}</td>
                    <td className="hidden px-4 py-2.5 text-zinc-500 md:table-cell" title={isFolder ? "" : formatDate(r.obj.lastModified)}>
                      {isFolder ? "—" : timeAgo(r.obj.lastModified)}
                    </td>
                    <td className="px-2 py-2.5" onClick={(e) => e.stopPropagation()}>
                      <Menu
                        items={[
                          { label: "Download", icon: Download, hidden: isFolder, onClick: () => (window.location.href = objectUrl(bucket.name, r.key, { download: true })) },
                          { label: "Download ZIP", icon: FileArchive, hidden: !isFolder, onClick: () => (window.location.href = zipUrl(bucket.name, { prefix: r.key })) },
                          { label: "Share", icon: Link2, hidden: !can.write, onClick: () => setShareTarget(r.key) },
                          { label: "Rename", icon: Pencil, hidden: !can.write, onClick: () => setRenaming(r.key) },
                          { label: "Move", icon: ArrowRightLeft, hidden: !can.write, onClick: () => setMoving([r.key]) },
                          { label: "Delete", icon: Trash2, danger: true, hidden: !can.write, onClick: () => setDeleting([r.key]) },
                        ]}
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          {listing.hasNextPage && (
            <div className="border-t border-zinc-200 p-3 text-center dark:border-zinc-800">
              <Button variant="secondary" size="sm" loading={listing.isFetchingNextPage} onClick={() => listing.fetchNextPage()}>
                Load more
              </Button>
            </div>
          )}
        </div>
      )}

      {dragging && (
        <div className="pointer-events-none absolute inset-0 z-20 grid place-items-center rounded-2xl border-2 border-dashed border-accent-500 bg-accent-50/80 dark:bg-accent-500/10">
          <div className="text-center">
            <UploadCloud className="mx-auto size-10 text-accent-600" />
            <p className="mt-2 font-medium">Drop to upload to /{prefix}</p>
          </div>
        </div>
      )}

      {open && (
        <ObjectPanel
          bucket={bucket}
          objectKey={open}
          onClose={() => setOpen(null)}
          onShare={setShareTarget}
          onDeleted={() => {
            setOpen(null);
            refresh();
          }}
        />
      )}
      <ShareDialog bucket={bucket.name} target={shareTarget} onClose={() => setShareTarget(null)} />
      <NewFolderDialog open={newFolder} bucket={bucket.name} prefix={prefix} onClose={() => setNewFolder(false)} onDone={refresh} />
      <RenameDialog bucket={bucket.name} target={renaming} onClose={() => setRenaming(null)} onDone={refresh} />
      <MoveDialog bucket={bucket.name} keys={moving} prefix={prefix} onClose={() => setMoving(null)} onDone={refresh} />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting)}
        loading={del.isPending}
        title={`Delete ${deleting?.length === 1 ? baseName(deleting[0]) : `${deleting?.length} items`}?`}
      >
        {deleting?.some((k) => k.endsWith("/")) && <p className="mb-2">Folders are deleted with everything inside them.</p>}
        {bucket.versioning === "Enabled" ? (
          <p>Versioning is on, so previous versions are kept and can be restored.</p>
        ) : (
          <p>This cannot be undone.</p>
        )}
      </ConfirmDialog>
    </div>
  );
}

function NewFolderDialog({ open, bucket, prefix, onClose, onDone }: { open: boolean; bucket: string; prefix: string; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const m = useMutation({
    mutationFn: () => createFolder(bucket, prefix + name.trim().replace(/^\/+|\/+$/g, "")),
    onSuccess: () => {
      setName("");
      onClose();
      onDone();
    },
  });
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New folder"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="new-folder" loading={m.isPending} disabled={!name.trim()}>
            Create
          </Button>
        </>
      }
    >
      <form
        id="new-folder"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
        className="space-y-3"
      >
        {m.error && <Alert>{m.error.message}</Alert>}
        <Field label="Folder name" value={name} onChange={(e) => setName(e.target.value)} autoFocus hint={`Created in /${prefix}`} />
      </form>
    </Modal>
  );
}

function RenameDialog({ bucket, target, onClose, onDone }: { bucket: string; target: string | null; onClose: () => void; onDone: () => void }) {
  const isFolder = target?.endsWith("/") ?? false;
  const parent = target ? target.slice(0, target.slice(0, isFolder ? -1 : undefined).lastIndexOf("/") + 1) : "";
  const [name, setName] = useState("");
  useEffect(() => setName(target ? baseName(target) : ""), [target]);
  const toast = useToast();
  const m = useMutation({
    mutationFn: () => copyObject(bucket, { sourceKey: target!, key: parent + name.trim() + (isFolder ? "/" : ""), move: true }),
    onSuccess: () => {
      toast("Renamed");
      onClose();
      onDone();
    },
  });
  return (
    <Modal
      open={target !== null}
      onClose={onClose}
      title="Rename"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="rename" loading={m.isPending} disabled={!name.trim() || name.includes("/")}>
            Rename
          </Button>
        </>
      }
    >
      <form
        id="rename"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
        className="space-y-3"
      >
        {m.error && <Alert>{m.error.message}</Alert>}
        <Field label="New name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </form>
    </Modal>
  );
}

function MoveDialog({ bucket, keys, prefix, onClose, onDone }: { bucket: string; keys: string[] | null; prefix: string; onClose: () => void; onDone: () => void }) {
  const [dest, setDest] = useState(prefix);
  const toast = useToast();
  const m = useMutation({
    mutationFn: async () => {
      let target = dest.trim().replace(/^\/+/, "");
      if (target && !target.endsWith("/")) target += "/";
      let n = 0;
      for (const k of keys ?? []) {
        const r = await copyObject(bucket, { sourceKey: k, key: target + baseName(k) + (k.endsWith("/") ? "/" : ""), move: true });
        n += r.count;
      }
      return n;
    },
    onSuccess: (n) => {
      toast(`Moved ${n} object${n === 1 ? "" : "s"}`);
      onClose();
      onDone();
    },
  });
  return (
    <Modal
      open={keys !== null}
      onClose={onClose}
      title={`Move ${keys?.length === 1 ? baseName(keys[0]) : `${keys?.length} items`}`}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="move" loading={m.isPending}>
            Move
          </Button>
        </>
      }
    >
      <form
        id="move"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
        className="space-y-3"
      >
        {m.error && <Alert>{m.error.message}</Alert>}
        <Field label="Destination folder" value={dest} onChange={(e) => setDest(e.target.value)} placeholder="(bucket root)" hint="For example photos/2026/. Leave empty for the bucket root." autoFocus />
      </form>
    </Modal>
  );
}
