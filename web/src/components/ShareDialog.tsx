import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Download, FolderOpen, Upload } from "lucide-react";
import { ApiError, createShare, type Share, type ShareType } from "../lib/api";
import { parseSize } from "../lib/format";
import { Alert, Button, CopyField, Field, Modal, Select } from "./ui";

const expiryOptions = [
  { label: "Never", value: "" },
  { label: "1 hour", value: String(3600) },
  { label: "1 day", value: String(86400) },
  { label: "7 days", value: String(7 * 86400) },
  { label: "30 days", value: String(30 * 86400) },
];

/**
 * Creates a share link for a file, or a folder/upload link for a prefix.
 * `target` is an object key, or a prefix ending in "/" ("" = bucket root).
 */
export function ShareDialog({ bucket, target, onClose }: { bucket: string; target: string | null; onClose: () => void }) {
  const isFolder = target !== null && (target === "" || target.endsWith("/"));
  const [type, setType] = useState<ShareType>(isFolder ? "folder" : "file");
  const [expiry, setExpiry] = useState(String(7 * 86400));
  const [password, setPassword] = useState("");
  const [maxDownloads, setMaxDownloads] = useState("");
  const [maxUpload, setMaxUpload] = useState("");
  const [note, setNote] = useState("");
  const [created, setCreated] = useState<Share>();
  const queryClient = useQueryClient();

  const mutation = useMutation({
    mutationFn: createShare,
    onSuccess: (s) => {
      setCreated(s);
      queryClient.invalidateQueries({ queryKey: ["shares"] });
    },
  });
  const fields = mutation.error instanceof ApiError ? mutation.error.fields : {};

  const close = () => {
    setCreated(undefined);
    setPassword("");
    setNote("");
    mutation.reset();
    onClose();
  };

  function submit(e: FormEvent) {
    e.preventDefault();
    const effectiveType = isFolder ? type : "file";
    mutation.mutate({
      type: effectiveType,
      bucket,
      key: target ?? "",
      expiresAt: expiry ? new Date(Date.now() + Number(expiry) * 1000).toISOString() : undefined,
      password: password || undefined,
      maxDownloads: maxDownloads ? Number(maxDownloads) : undefined,
      maxUploadBytes: effectiveType === "upload" ? parseSize(maxUpload) : undefined,
      note: note || undefined,
    });
  }

  const name = target === "" ? bucket : target;

  return (
    <Modal
      open={target !== null}
      onClose={close}
      title={created ? "Link created" : "Create share link"}
      footer={
        created ? (
          <Button onClick={close}>Done</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" form="share-form" loading={mutation.isPending}>
              Create link
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-4">
          <CopyField label="Share link" value={created.url} />
          <p className="text-sm text-zinc-500 dark:text-zinc-400">
            Anyone with this link {created.type === "upload" ? "can upload files" : "can view and download"}
            {created.hasPassword ? " after entering the password" : ""}. Manage it on the Share links page.
          </p>
        </div>
      ) : (
        <form id="share-form" onSubmit={submit} className="space-y-4">
          <p className="truncate text-sm text-zinc-500 dark:text-zinc-400" title={name ?? ""}>
            Sharing <span className="font-medium text-zinc-900 dark:text-zinc-100">{name}</span>
          </p>
          {mutation.error && Object.keys(fields).length === 0 && <Alert>{mutation.error.message}</Alert>}
          {isFolder && (
            <div className="grid grid-cols-2 gap-2">
              {(
                [
                  ["folder", FolderOpen, "View & download", "Browse and download files"],
                  ["upload", Upload, "File request", "Others can upload, not see"],
                ] as const
              ).map(([t, Icon, title, desc]) => (
                <button
                  key={t}
                  type="button"
                  onClick={() => setType(t)}
                  className={`rounded-xl border p-3 text-left transition ${
                    type === t
                      ? "border-accent-500 bg-accent-50 dark:bg-accent-500/10"
                      : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700"
                  }`}
                >
                  <Icon className="mb-2 size-4 text-accent-600" />
                  <span className="block text-sm font-medium">{title}</span>
                  <span className="block text-xs text-zinc-500">{desc}</span>
                </button>
              ))}
            </div>
          )}
          <Select label="Expires" value={expiry} onChange={(e) => setExpiry(e.target.value)}>
            {expiryOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </Select>
          <Field
            label="Password (optional)"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
          {type !== "upload" || !isFolder ? (
            <Field
              label="Download limit (optional)"
              type="number"
              min={1}
              value={maxDownloads}
              onChange={(e) => setMaxDownloads(e.target.value)}
              error={fields.maxDownloads}
              hint={
                <span className="inline-flex items-center gap-1">
                  <Download className="size-3" /> The link stops working after this many downloads.
                </span>
              }
            />
          ) : (
            <Field
              label="Maximum file size (optional)"
              value={maxUpload}
              onChange={(e) => setMaxUpload(e.target.value)}
              placeholder="e.g. 500 MB"
              error={fields.maxUploadBytes}
            />
          )}
          <Field label="Note for recipients (optional)" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500} />
        </form>
      )}
    </Modal>
  );
}
