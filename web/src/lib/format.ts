export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let value = bytes / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[i]}`;
}

export function formatDuration(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function formatDate(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function timeAgo(iso?: string): string {
  if (!iso) return "never";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 0) return "in the future";
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`;
  return formatDate(iso);
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat().format(n);
}

/** Parses "10 GB"-style sizes; returns undefined for empty input. */
export function parseSize(input: string): number | undefined {
  const m = input.trim().match(/^(\d+(?:\.\d+)?)\s*(b|kb|mb|gb|tb)?$/i);
  if (!m) return undefined;
  const mult: Record<string, number> = { b: 1, kb: 1024, mb: 1024 ** 2, gb: 1024 ** 3, tb: 1024 ** 4 };
  return Math.round(parseFloat(m[1]) * mult[(m[2] ?? "b").toLowerCase()]);
}

export function baseName(key: string): string {
  const trimmed = key.endsWith("/") ? key.slice(0, -1) : key;
  return trimmed.slice(trimmed.lastIndexOf("/") + 1);
}

export type FileKind = "image" | "video" | "audio" | "pdf" | "text" | "archive" | "other";

const textExt = /\.(txt|md|json|ya?ml|xml|csv|tsv|log|ini|toml|conf|env|sh|py|go|rs|js|ts|tsx|jsx|css|html?|sql|c|h|cpp|java|kt|rb|php)$/i;

export function fileKind(name: string, contentType = ""): FileKind {
  const ct = contentType.toLowerCase();
  if (ct.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg|avif|bmp|ico)$/i.test(name)) return "image";
  if (ct.startsWith("video/") || /\.(mp4|webm|mov|mkv|m4v)$/i.test(name)) return "video";
  if (ct.startsWith("audio/") || /\.(mp3|wav|ogg|flac|m4a|aac)$/i.test(name)) return "audio";
  if (ct === "application/pdf" || /\.pdf$/i.test(name)) return "pdf";
  if (ct.startsWith("text/") || ct === "application/json" || textExt.test(name)) return "text";
  if (/\.(zip|tar|gz|tgz|bz2|xz|7z|rar)$/i.test(name)) return "archive";
  return "other";
}

/** "1 object", "3 objects" */
export function plural(n: number, word: string): string {
  return `${formatNumber(n)} ${word}${n === 1 ? "" : "s"}`;
}
