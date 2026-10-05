import { useRef, useState, type DragEvent, type FormEvent } from "react";
import { useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, FileArchive, Folder, Lock, UploadCloud } from "lucide-react";
import {
  ApiError,
  getPublicShare,
  listPublicShare,
  publicDownloadUrl,
  publicUploadUrl,
  publicZipUrl,
  unlockShare,
  type PublicShare,
} from "../lib/api";
import { filesFromDrop, putWithProgress } from "../lib/upload";
import { formatBytes, formatDate } from "../lib/format";
import { FileTypeIcon, Preview } from "../components/Preview";
import { Alert, Button, Card, Field, Logo, Spinner } from "../components/ui";

export function PublicSharePage() {
  const { token = "" } = useParams();
  const share = useQuery({ queryKey: ["public-share", token], queryFn: () => getPublicShare(token), retry: false });

  let body;
  if (share.isPending) {
    body = (
      <div className="grid place-items-center py-24">
        <Spinner />
      </div>
    );
  } else if (share.error) {
    const gone = share.error instanceof ApiError && (share.error.status === 410 || share.error.status === 404);
    body = (
      <Card className="mx-auto max-w-md p-8 text-center">
        <h1 className="text-lg font-semibold">{gone ? "This link is no longer available" : "Something went wrong"}</h1>
        <p className="mt-2 text-sm text-zinc-500">{share.error.message}</p>
      </Card>
    );
  } else if (share.data.requiresPassword && !share.data.unlocked) {
    body = <Unlock token={token} name={share.data.name} />;
  } else {
    const s = share.data;
    body =
      s.type === "file" ? <FileView token={token} share={s} /> : s.type === "folder" ? <FolderView token={token} share={s} /> : <UploadView token={token} share={s} />;
  }

  return (
    <div className="min-h-screen">
      <header className="border-b border-zinc-200 bg-white/70 backdrop-blur dark:border-zinc-800 dark:bg-zinc-900/60">
        <div className="mx-auto flex h-14 max-w-5xl items-center gap-2.5 px-4">
          <Logo className="size-6" />
          <span className="font-semibold">{share.data?.siteName ?? "Shared files"}</span>
        </div>
      </header>
      <main className="mx-auto max-w-5xl px-4 py-10">{body}</main>
    </div>
  );
}

function ShareMeta({ share }: { share: PublicShare }) {
  const bits = [];
  if (share.expiresAt) bits.push(`Expires ${formatDate(share.expiresAt)}`);
  if (share.downloadsLeft !== undefined) bits.push(`${share.downloadsLeft} download${share.downloadsLeft === 1 ? "" : "s"} left`);
  return (
    <>
      {share.note && (
        <p className="mt-3 whitespace-pre-wrap rounded-lg bg-zinc-100 px-3 py-2 text-sm dark:bg-zinc-800">{share.note}</p>
      )}
      {bits.length > 0 && <p className="mt-2 text-xs text-zinc-500">{bits.join(" · ")}</p>}
    </>
  );
}

function Unlock({ token, name }: { token: string; name: string }) {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const m = useMutation({
    mutationFn: () => unlockShare(token, password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["public-share", token] }),
  });
  return (
    <Card className="mx-auto max-w-sm p-6">
      <Lock className="mx-auto size-8 text-accent-600" />
      <h1 className="mt-3 text-center font-semibold">{name} is password protected</h1>
      <form
        className="mt-5 space-y-4"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        {m.error && <Alert>{m.error.message}</Alert>}
        <Field label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus required />
        <Button type="submit" className="w-full" loading={m.isPending}>
          Unlock
        </Button>
      </form>
    </Card>
  );
}

function FileView({ token, share }: { token: string; share: PublicShare }) {
  const f = share.file!;
  return (
    <Card className="mx-auto max-w-3xl p-6">
      <div className="flex flex-wrap items-start gap-4">
        <FileTypeIcon name={f.name} contentType={f.contentType} className="size-10" />
        <div className="min-w-0 flex-1">
          <h1 className="break-all text-lg font-semibold">{f.name}</h1>
          <p className="text-sm text-zinc-500">
            {formatBytes(f.size)} · {formatDate(f.lastModified)}
          </p>
        </div>
        <a href={publicDownloadUrl(token, { download: true })}>
          <Button icon={Download}>Download</Button>
        </a>
      </div>
      <ShareMeta share={share} />
      <div className="mt-6">
        <Preview url={publicDownloadUrl(token)} name={f.name} contentType={f.contentType} size={f.size} />
      </div>
    </Card>
  );
}

function FolderView({ token, share }: { token: string; share: PublicShare }) {
  const [path, setPath] = useState("");
  const [preview, setPreview] = useState<{ path: string; name: string; contentType: string; size: number } | null>(null);
  const listing = useQuery({ queryKey: ["public-list", token, path], queryFn: () => listPublicShare(token, path) });
  const crumbs = path.split("/").filter(Boolean);

  return (
    <Card className="p-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="flex items-center gap-2 text-lg font-semibold">
            <Folder className="size-5 text-accent-600" />
            {share.name}
          </h1>
          <ShareMeta share={share} />
        </div>
        <a href={publicZipUrl(token)}>
          <Button variant="secondary" icon={FileArchive}>
            Download all
          </Button>
        </a>
      </div>
      <nav className="mt-5 flex flex-wrap items-center gap-1 text-sm">
        <button type="button" className="rounded px-1.5 py-1 font-medium hover:bg-zinc-100 dark:hover:bg-zinc-800" onClick={() => setPath("")}>
          {share.name}
        </button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="size-3.5 text-zinc-400" />
            <button
              type="button"
              className="rounded px-1.5 py-1 hover:bg-zinc-100 dark:hover:bg-zinc-800"
              onClick={() => setPath(crumbs.slice(0, i + 1).join("/") + "/")}
            >
              {c}
            </button>
          </span>
        ))}
      </nav>
      {listing.error && <Alert>{listing.error.message}</Alert>}
      {listing.isPending ? (
        <Spinner />
      ) : (
        <ul className="mt-2 divide-y divide-zinc-100 dark:divide-zinc-800">
          {listing.data?.folders.map((f) => (
            <li key={f}>
              <button type="button" onClick={() => setPath(f)} className="flex w-full items-center gap-3 px-2 py-2.5 text-left text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800/40">
                <FileTypeIcon name={f} folder />
                <span className="font-medium">{f.slice(path.length).replace(/\/$/, "")}</span>
              </button>
            </li>
          ))}
          {listing.data?.files.map((f) => (
            <li key={f.path} className="flex items-center gap-3 px-2 py-2.5 text-sm">
              <FileTypeIcon name={f.name} contentType={f.contentType} />
              <button type="button" className="min-w-0 flex-1 truncate text-left font-medium hover:underline" onClick={() => setPreview(f)}>
                {f.name}
              </button>
              <span className="hidden tabular-nums text-zinc-500 sm:inline">{formatBytes(f.size)}</span>
              <a href={publicDownloadUrl(token, { path: f.path, download: true })} aria-label={`Download ${f.name}`} className="rounded-md p-1.5 text-zinc-500 hover:bg-zinc-100 dark:hover:bg-zinc-800">
                <Download className="size-4" />
              </a>
            </li>
          ))}
          {listing.data && listing.data.files.length + listing.data.folders.length === 0 && (
            <li className="py-10 text-center text-sm text-zinc-500">This folder is empty.</li>
          )}
        </ul>
      )}
      {preview && (
        <div className="mt-6 border-t border-zinc-200 pt-6 dark:border-zinc-800">
          <div className="mb-3 flex items-center justify-between">
            <h2 className="truncate font-medium">{preview.name}</h2>
            <Button size="sm" variant="ghost" onClick={() => setPreview(null)}>
              Close preview
            </Button>
          </div>
          <Preview url={publicDownloadUrl(token, { path: preview.path })} name={preview.name} contentType={preview.contentType} size={preview.size} />
        </div>
      )}
    </Card>
  );
}

type UploadRow = { name: string; size: number; loaded: number; state: "uploading" | "done" | "error"; error?: string };

function UploadView({ token, share }: { token: string; share: PublicShare }) {
  const input = useRef<HTMLInputElement>(null);
  const [rows, setRows] = useState<UploadRow[]>([]);
  const [dragging, setDragging] = useState(false);

  const start = async (files: { file: File; path: string }[]) => {
    const offset = rows.length;
    setRows((r) => [...r, ...files.map(({ file, path }) => ({ name: path, size: file.size, loaded: 0, state: "uploading" as const }))]);
    const set = (i: number, p: Partial<UploadRow>) => setRows((r) => r.map((x, j) => (j === offset + i ? { ...x, ...p } : x)));
    // Upload sequentially to keep things gentle on slow connections.
    for (const [i, { file, path }] of files.entries()) {
      if (share.maxUploadBytes && file.size > share.maxUploadBytes) {
        set(i, { state: "error", error: `Larger than the ${formatBytes(share.maxUploadBytes)} limit` });
        continue;
      }
      try {
        await putWithProgress(publicUploadUrl(token, path), file, file.type || "application/octet-stream", (n) => set(i, { loaded: n }), new AbortController().signal);
        set(i, { state: "done", loaded: file.size });
      } catch (e) {
        set(i, { state: "error", error: (e as Error).message });
      }
    }
  };

  const onDrop = async (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    start(await filesFromDrop(e.dataTransfer));
  };

  return (
    <Card className="mx-auto max-w-2xl p-6">
      <h1 className="text-lg font-semibold">Upload files to {share.name}</h1>
      <p className="text-sm text-zinc-500">Files you upload are sent to the owner. You won't be able to see other uploads.</p>
      <ShareMeta share={share} />
      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={onDrop}
        onClick={() => input.current?.click()}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && input.current?.click()}
        className={`mt-6 cursor-pointer rounded-2xl border-2 border-dashed px-6 py-12 text-center transition ${
          dragging ? "border-accent-500 bg-accent-50 dark:bg-accent-500/10" : "border-zinc-300 hover:border-accent-400 dark:border-zinc-700"
        }`}
      >
        <UploadCloud className="mx-auto size-10 text-accent-600" />
        <p className="mt-3 font-medium">Drop files here or click to choose</p>
        {share.maxUploadBytes && <p className="mt-1 text-xs text-zinc-500">Up to {formatBytes(share.maxUploadBytes)} per file</p>}
      </div>
      <input
        ref={input}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          start(Array.from(e.target.files ?? []).map((file) => ({ file, path: file.name })));
          e.target.value = "";
        }}
      />
      {rows.length > 0 && (
        <ul className="mt-5 space-y-2">
          {rows.map((r, i) => (
            <li key={i} className="rounded-lg border border-zinc-200 px-3 py-2 text-sm dark:border-zinc-800">
              <div className="flex justify-between gap-3">
                <span className="truncate">{r.name}</span>
                <span className={`shrink-0 text-xs ${r.state === "error" ? "text-red-600" : r.state === "done" ? "text-emerald-600" : "text-zinc-500"}`}>
                  {r.state === "done" ? "Uploaded" : r.state === "error" ? r.error : `${Math.round((r.loaded / Math.max(r.size, 1)) * 100)}%`}
                </span>
              </div>
              {r.state === "uploading" && (
                <div className="mt-1.5 h-1 overflow-hidden rounded-full bg-zinc-100 dark:bg-zinc-800">
                  <div className="h-full bg-accent-500 transition-all" style={{ width: `${(r.loaded / Math.max(r.size, 1)) * 100}%` }} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
