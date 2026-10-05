import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Check, FolderOpen, Link2, Lock, Trash2, Upload, File as FileIcon } from "lucide-react";
import { deleteShare, listShares, updateShare, type Share } from "../lib/api";
import { formatDate, timeAgo } from "../lib/format";
import { useCan } from "../lib/session";
import { Menu } from "../components/Menu";
import { Alert, Badge, ConfirmDialog, CopyButton, EmptyState, PageHeader, Spinner, Table, td, th, useToast } from "../components/ui";

function status(s: Share): { label: string; tone: "green" | "amber" | "red" | "neutral" } {
  if (s.disabled) return { label: "Disabled", tone: "neutral" };
  if (s.expiresAt && new Date(s.expiresAt) < new Date()) return { label: "Expired", tone: "red" };
  if (s.maxDownloads && s.downloads >= s.maxDownloads) return { label: "Limit reached", tone: "amber" };
  return { label: "Active", tone: "green" };
}

const typeIcon = { file: FileIcon, folder: FolderOpen, upload: Upload };

export function SharesPage() {
  const can = useCan();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [deleting, setDeleting] = useState<Share | null>(null);
  const { data, isPending, error } = useQuery({ queryKey: ["shares"], queryFn: () => listShares() });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["shares"] });

  const toggle = useMutation({
    mutationFn: (s: Share) => updateShare(s.id, { disabled: !s.disabled }),
    onSuccess: (s) => {
      toast(s.disabled ? "Link disabled" : "Link enabled");
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });
  const del = useMutation({
    mutationFn: (s: Share) => deleteShare(s.id),
    onSuccess: () => {
      toast("Link deleted");
      setDeleting(null);
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Share links"
        description={can.admin ? "All public links on this server." : "Public links you have created."}
      />
      {error && <Alert>{error.message}</Alert>}
      {isPending ? (
        <div className="grid place-items-center py-20">
          <Spinner />
        </div>
      ) : data?.length === 0 ? (
        <EmptyState icon={Link2} title="No share links yet">
          Open a bucket, then use Share on a file or folder to create a public link or a file request.
        </EmptyState>
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>Shared item</th>
              <th className={th}>Status</th>
              <th className={`${th} hidden md:table-cell`}>Activity</th>
              <th className={`${th} hidden lg:table-cell`}>Expires</th>
              <th className="w-20" />
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {data?.map((s) => {
              const st = status(s);
              const Icon = typeIcon[s.type];
              const folder = s.type === "file" ? s.key.slice(0, s.key.lastIndexOf("/") + 1) : s.key;
              return (
                <tr key={s.id}>
                  <td className={td}>
                    <div className="flex items-center gap-2.5">
                      <Icon className="size-4 shrink-0 text-accent-600" />
                      <div className="min-w-0">
                        <Link
                          to={`/buckets/${s.bucket}/${folder.split("/").map(encodeURIComponent).join("/")}`}
                          className="block truncate font-medium hover:underline"
                          title={`${s.bucket}/${s.key}`}
                        >
                          {s.bucket}/{s.key}
                        </Link>
                        <p className="flex items-center gap-1.5 text-xs text-zinc-500">
                          {s.type === "upload" ? "File request" : s.type === "folder" ? "Folder" : "File"}
                          {s.hasPassword && (
                            <span className="inline-flex items-center gap-0.5">
                              · <Lock className="size-3" /> password
                            </span>
                          )}
                          {can.admin && s.createdByName && <span>· by {s.createdByName}</span>}
                        </p>
                      </div>
                    </div>
                  </td>
                  <td className={td}>
                    <Badge tone={st.tone}>{st.label}</Badge>
                  </td>
                  <td className={`${td} hidden text-zinc-500 md:table-cell`}>
                    <span className="tabular-nums">
                      {s.views} views · {s.downloads}
                      {s.maxDownloads ? `/${s.maxDownloads}` : ""} downloads
                    </span>
                    <p className="text-xs">last used {timeAgo(s.lastAccessedAt)}</p>
                  </td>
                  <td className={`${td} hidden text-zinc-500 lg:table-cell`}>{s.expiresAt ? formatDate(s.expiresAt) : "Never"}</td>
                  <td className={td}>
                    <div className="flex items-center justify-end gap-1">
                      <CopyButton value={s.url} label="Copy link" />
                      <Menu
                        items={[
                          { label: "Open link", icon: Link2, onClick: () => window.open(s.url, "_blank", "noopener") },
                          { label: s.disabled ? "Enable" : "Disable", icon: s.disabled ? Check : Ban, onClick: () => toggle.mutate(s) },
                          { label: "Delete", icon: Trash2, danger: true, onClick: () => setDeleting(s) },
                        ]}
                      />
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting)}
        loading={del.isPending}
        title="Delete share link?"
      >
        The link will stop working immediately. The shared files are not affected.
      </ConfirmDialog>
    </div>
  );
}
