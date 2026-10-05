import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, ChevronDown, ChevronUp, CircleX, Upload, X } from "lucide-react";
import { isAbort, uploadObject } from "../lib/upload";
import { formatBytes } from "../lib/format";
import { IconButton } from "./ui";

type Status = "queued" | "uploading" | "done" | "error" | "canceled";

type Item = {
  id: number;
  bucket: string;
  key: string;
  file: File;
  loaded: number;
  status: Status;
  error?: string;
  controller: AbortController;
};

type Ctx = { enqueue: (bucket: string, files: { file: File; key: string }[]) => void };

const UploadContext = createContext<Ctx>({ enqueue: () => {} });
export const useUploads = () => useContext(UploadContext);

const FILE_CONCURRENCY = 3;

export function UploadProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([]);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  const nextId = useRef(1);
  const running = useRef(0);
  const queryClient = useQueryClient();

  const patch = (id: number, p: Partial<Item>) => setItems((list) => list.map((it) => (it.id === id ? { ...it, ...p } : it)));

  const pump = useCallback(() => {
    while (running.current < FILE_CONCURRENCY) {
      const item = itemsRef.current.find((it) => it.status === "queued");
      if (!item) return;
      running.current++;
      item.status = "uploading"; // mark synchronously so the loop does not pick it again
      patch(item.id, { status: "uploading" });
      uploadObject(item.bucket, item.key, item.file, (loaded) => patch(item.id, { loaded }), item.controller.signal)
        .then(() => {
          patch(item.id, { status: "done", loaded: item.file.size });
          queryClient.invalidateQueries({ queryKey: ["objects", item.bucket] });
          queryClient.invalidateQueries({ queryKey: ["buckets"] });
        })
        .catch((e) => patch(item.id, isAbort(e) ? { status: "canceled" } : { status: "error", error: (e as Error).message }))
        .finally(() => {
          running.current--;
          setTimeout(pump, 0);
        });
    }
  }, [queryClient]);

  useEffect(() => {
    pump();
  }, [items.length, pump]);

  const enqueue = useCallback((bucket: string, files: { file: File; key: string }[]) => {
    setItems((list) => [
      ...list,
      ...files.map(({ file, key }) => ({
        id: nextId.current++,
        bucket,
        key,
        file,
        loaded: 0,
        status: "queued" as Status,
        controller: new AbortController(),
      })),
    ]);
  }, []);

  return (
    <UploadContext.Provider value={{ enqueue }}>
      {children}
      <UploadPanel
        items={items}
        onCancel={(id) => {
          const it = items.find((x) => x.id === id);
          if (!it) return;
          if (it.status === "queued") patch(id, { status: "canceled" });
          it.controller.abort();
        }}
        onClear={() => setItems((list) => list.filter((it) => it.status === "uploading" || it.status === "queued"))}
      />
    </UploadContext.Provider>
  );
}

function UploadPanel({ items, onCancel, onClear }: { items: Item[]; onCancel: (id: number) => void; onClear: () => void }) {
  const [collapsed, setCollapsed] = useState(false);
  if (items.length === 0) return null;
  const active = items.filter((i) => i.status === "uploading" || i.status === "queued");
  const total = items.reduce((a, i) => a + i.file.size, 0);
  const loaded = items.reduce((a, i) => a + (i.status === "done" ? i.file.size : i.loaded), 0);
  const pct = total ? Math.round((loaded / total) * 100) : 100;

  return (
    <div className="fixed bottom-4 left-4 right-4 z-40 overflow-hidden rounded-2xl border border-zinc-200 bg-white shadow-xl sm:left-auto sm:w-96 dark:border-zinc-800 dark:bg-zinc-900">
      <div className="flex items-center gap-2 border-b border-zinc-200 px-4 py-2.5 dark:border-zinc-800">
        <Upload className="size-4 text-accent-600" />
        <span className="flex-1 text-sm font-medium">
          {active.length > 0 ? `Uploading ${active.length} file${active.length === 1 ? "" : "s"} · ${pct}%` : "Uploads complete"}
        </span>
        <IconButton icon={collapsed ? ChevronUp : ChevronDown} label={collapsed ? "Expand" : "Collapse"} onClick={() => setCollapsed(!collapsed)} />
        {active.length === 0 && <IconButton icon={X} label="Clear" onClick={onClear} />}
      </div>
      {active.length > 0 && (
        <div className="h-1 bg-zinc-100 dark:bg-zinc-800">
          <div className="h-full bg-accent-500 transition-all" style={{ width: `${pct}%` }} />
        </div>
      )}
      {!collapsed && (
        <ul className="max-h-72 divide-y divide-zinc-100 overflow-y-auto dark:divide-zinc-800">
          {items.map((it) => {
            const p = it.file.size ? Math.round((it.loaded / it.file.size) * 100) : 100;
            return (
              <li key={it.id} className="flex items-center gap-3 px-4 py-2">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm" title={it.key}>
                    {it.key}
                  </p>
                  <p className={`text-xs ${it.status === "error" ? "text-red-600 dark:text-red-400" : "text-zinc-500"}`}>
                    {it.status === "uploading" && `${formatBytes(it.loaded)} of ${formatBytes(it.file.size)} · ${p}%`}
                    {it.status === "queued" && `Waiting · ${formatBytes(it.file.size)}`}
                    {it.status === "done" && formatBytes(it.file.size)}
                    {it.status === "canceled" && "Canceled"}
                    {it.status === "error" && it.error}
                  </p>
                </div>
                {it.status === "done" && <CheckCircle2 className="size-4 text-emerald-500" />}
                {it.status === "error" && <CircleX className="size-4 text-red-500" />}
                {(it.status === "uploading" || it.status === "queued") && (
                  <IconButton icon={X} label="Cancel upload" onClick={() => onCancel(it.id)} />
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
