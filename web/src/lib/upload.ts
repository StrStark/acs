import { ApiError, api, encodeKey } from "./api";

const MULTIPART_THRESHOLD = 64 * 1024 * 1024;
const PART_SIZE = 16 * 1024 * 1024;
const PART_CONCURRENCY = 4;

class AbortError extends Error {
  constructor() {
    super("Upload canceled");
  }
}

/** PUTs a body with progress reporting; resolves with the parsed JSON response. */
export function putWithProgress<T>(
  url: string,
  body: Blob,
  contentType: string,
  onProgress: (loaded: number) => void,
  signal: AbortSignal,
): Promise<T> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.withCredentials = true;
    if (contentType) xhr.setRequestHeader("Content-Type", contentType);
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () => {
      let data: unknown = null;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : null;
      } catch {
        // ignore
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data as T);
      } else {
        const p = (data ?? {}) as { detail?: string; errors?: Record<string, string> };
        reject(new ApiError(xhr.status, p.detail ?? xhr.statusText, p.errors));
      }
    };
    xhr.onerror = () => reject(new Error("Network error"));
    xhr.onabort = () => reject(new AbortError());
    signal.addEventListener("abort", () => xhr.abort());
    xhr.send(body);
  });
}

export async function uploadObject(
  bucket: string,
  key: string,
  file: File,
  onProgress: (loaded: number) => void,
  signal: AbortSignal,
): Promise<void> {
  const type = file.type || "application/octet-stream";
  if (file.size <= MULTIPART_THRESHOLD) {
    await putWithProgress(`/api/v1/buckets/${bucket}/objects/${encodeKey(key)}`, file, type, onProgress, signal);
    return;
  }

  const { uploadId } = await api<{ uploadId: string }>(`/buckets/${bucket}/uploads`, {
    method: "POST",
    body: { key, contentType: type },
  });
  const count = Math.ceil(file.size / PART_SIZE);
  const loaded = new Array<number>(count).fill(0);
  const etags = new Array<string>(count);
  const report = () => onProgress(loaded.reduce((a, b) => a + b, 0));
  let next = 0;

  const worker = async () => {
    while (next < count) {
      const i = next++;
      const blob = file.slice(i * PART_SIZE, Math.min(file.size, (i + 1) * PART_SIZE));
      const url = `/api/v1/buckets/${bucket}/uploads/${uploadId}/parts/${i + 1}?key=${encodeURIComponent(key)}`;
      // Retry each part a couple of times on transient failures.
      for (let attempt = 1; ; attempt++) {
        try {
          const res = await putWithProgress<{ etag: string }>(url, blob, "application/octet-stream", (n) => {
            loaded[i] = n;
            report();
          }, signal);
          etags[i] = res.etag;
          break;
        } catch (e) {
          if (e instanceof AbortError || attempt >= 3) throw e;
          await new Promise((r) => setTimeout(r, 1000 * attempt));
        }
      }
    }
  };

  try {
    await Promise.all(Array.from({ length: Math.min(PART_CONCURRENCY, count) }, worker));
    await api(`/buckets/${bucket}/uploads/${uploadId}/complete`, {
      method: "POST",
      body: { key, parts: etags.map((etag, i) => ({ partNumber: i + 1, etag })) },
    });
  } catch (e) {
    api(`/buckets/${bucket}/uploads/${uploadId}?key=${encodeURIComponent(key)}`, { method: "DELETE" }).catch(() => {});
    throw e;
  }
}

export function isAbort(e: unknown) {
  return e instanceof AbortError;
}

/** Collects files (with relative paths) from a drop, descending into folders. */
export async function filesFromDrop(dt: DataTransfer): Promise<{ file: File; path: string }[]> {
  const out: { file: File; path: string }[] = [];
  const entries = Array.from(dt.items)
    .map((it) => (it.kind === "file" ? it.webkitGetAsEntry() : null))
    .filter((e): e is FileSystemEntry => e !== null);

  if (entries.length === 0) {
    return Array.from(dt.files).map((file) => ({ file, path: file.name }));
  }

  const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej));
      out.push({ file, path: prefix + entry.name });
      return;
    }
    const reader = (entry as FileSystemDirectoryEntry).createReader();
    // readEntries returns results in batches until it yields an empty array.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej));
      if (batch.length === 0) break;
      for (const child of batch) await walk(child, `${prefix}${entry.name}/`);
    }
  };
  for (const e of entries) await walk(e, "");
  return out;
}
