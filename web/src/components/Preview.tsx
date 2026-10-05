import { useEffect, useState } from "react";
import { FileArchive, FileAudio, FileCode, FileImage, FileText, FileVideo, File as FileIcon, Folder, type LucideIcon } from "lucide-react";
import { fileKind, type FileKind } from "../lib/format";
import { Spinner } from "./ui";

const icons: Record<FileKind, LucideIcon> = {
  image: FileImage,
  video: FileVideo,
  audio: FileAudio,
  pdf: FileText,
  text: FileCode,
  archive: FileArchive,
  other: FileIcon,
};

export function FileTypeIcon({ name, contentType, folder, className = "size-4" }: { name: string; contentType?: string; folder?: boolean; className?: string }) {
  if (folder) return <Folder className={`${className} text-accent-500`} />;
  const Icon = icons[fileKind(name, contentType)];
  return <Icon className={`${className} text-zinc-400`} />;
}

const MAX_TEXT_PREVIEW = 512 * 1024;

/**
 * Renders an inline preview. Images, video and audio load directly; PDFs and
 * text are fetched and shown from a blob/string so served content never runs
 * as a document in the panel's origin.
 */
export function Preview({ url, name, contentType, size }: { url: string; name: string; contentType?: string; size: number }) {
  const kind = fileKind(name, contentType);
  const [text, setText] = useState<string>();
  const [blobUrl, setBlobUrl] = useState<string>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    setText(undefined);
    setBlobUrl(undefined);
    setError(undefined);
    if (kind === "text" && size <= MAX_TEXT_PREVIEW) {
      const ctrl = new AbortController();
      fetch(url, { signal: ctrl.signal, credentials: "same-origin" })
        .then((r) => (r.ok ? r.text() : Promise.reject(new Error(r.statusText))))
        .then(setText)
        .catch((e) => e.name !== "AbortError" && setError(e.message));
      return () => ctrl.abort();
    }
    if (kind === "pdf") {
      let objectUrl: string | undefined;
      const ctrl = new AbortController();
      fetch(url, { signal: ctrl.signal, credentials: "same-origin" })
        .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(r.statusText))))
        .then((b) => {
          objectUrl = URL.createObjectURL(new Blob([b], { type: "application/pdf" }));
          setBlobUrl(objectUrl);
        })
        .catch((e) => e.name !== "AbortError" && setError(e.message));
      return () => {
        ctrl.abort();
        if (objectUrl) URL.revokeObjectURL(objectUrl);
      };
    }
  }, [url, kind, size]);

  const frame = "overflow-hidden rounded-xl border border-zinc-200 bg-zinc-50 dark:border-zinc-800 dark:bg-zinc-950";
  if (error) return <p className="text-sm text-red-600">Preview failed: {error}</p>;

  switch (kind) {
    case "image":
      return (
        <div className={`${frame} grid place-items-center p-2`}>
          <img src={url} alt={name} className="max-h-[60vh] max-w-full object-contain" />
        </div>
      );
    case "video":
      return <video src={url} controls className={`${frame} max-h-[60vh] w-full bg-black`} />;
    case "audio":
      return <audio src={url} controls className="w-full" />;
    case "pdf":
      return blobUrl ? (
        <iframe src={blobUrl} title={name} className={`${frame} h-[60vh] w-full`} />
      ) : (
        <div className="grid h-40 place-items-center">
          <Spinner />
        </div>
      );
    case "text":
      if (size > MAX_TEXT_PREVIEW) return <NoPreview reason="This file is too large to preview." />;
      return text === undefined ? (
        <div className="grid h-40 place-items-center">
          <Spinner />
        </div>
      ) : (
        <pre className={`${frame} max-h-[60vh] overflow-auto p-4 text-xs leading-relaxed`}>{text}</pre>
      );
    default:
      return <NoPreview reason="No preview available for this file type." />;
  }
}

function NoPreview({ reason }: { reason: string }) {
  return (
    <div className="rounded-xl border border-dashed border-zinc-300 p-8 text-center text-sm text-zinc-500 dark:border-zinc-700">{reason}</div>
  );
}
