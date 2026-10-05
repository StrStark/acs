import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { ApiError, createKey, deleteKey, getSettings, listBuckets, listKeys, listUsers, type AccessKey } from "../lib/api";
import { formatDate, timeAgo } from "../lib/format";
import { useCan, useSession } from "../lib/session";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  CopyField,
  EmptyState,
  Field,
  IconButton,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Table,
  Toggle,
  td,
  th,
  useToast,
} from "../components/ui";

const permLabel = { read: "Read only", readwrite: "Read & write", full: "Full access" };

export function KeysPage() {
  const can = useCan();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [showAll, setShowAll] = useState(false);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<AccessKey | null>(null);
  const keys = useQuery({ queryKey: ["keys", showAll], queryFn: () => listKeys(showAll) });
  const del = useMutation({
    mutationFn: (k: AccessKey) => deleteKey(k.id),
    onSuccess: () => {
      toast("Access key revoked");
      setDeleting(null);
      queryClient.invalidateQueries({ queryKey: ["keys"] });
    },
    onError: (e) => toast(e.message, "error"),
  });

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Access keys"
        description="Credentials for S3 clients and the REST API."
        actions={
          <>
            {can.admin && (
              <div className="w-44">
                <Toggle checked={showAll} onChange={setShowAll} label="All users" />
              </div>
            )}
            <Button icon={Plus} onClick={() => setCreating(true)}>
              New key
            </Button>
          </>
        }
      />
      {keys.error && <Alert>{keys.error.message}</Alert>}
      {keys.isPending ? (
        <div className="grid place-items-center py-20">
          <Spinner />
        </div>
      ) : keys.data?.length === 0 ? (
        <EmptyState icon={KeyRound} title="No access keys" action={<Button icon={Plus} onClick={() => setCreating(true)}>Create key</Button>}>
          Create a key to connect aws-cli, rclone, SDKs or your own apps.
        </EmptyState>
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>Name</th>
              <th className={th}>Access</th>
              <th className={`${th} hidden md:table-cell`}>Last used</th>
              <th className={`${th} hidden lg:table-cell`}>Expires</th>
              <th className="w-12" />
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {keys.data?.map((k) => (
              <tr key={k.id}>
                <td className={td}>
                  <p className="font-medium">{k.name}</p>
                  <p className="font-mono text-xs text-zinc-500">
                    {k.id}
                    {showAll && ` · ${k.username}`}
                  </p>
                </td>
                <td className={td}>
                  <Badge tone={k.permission === "full" ? "amber" : k.permission === "read" ? "neutral" : "accent"}>{permLabel[k.permission]}</Badge>
                  <p className="mt-1 text-xs text-zinc-500">{k.buckets.includes("*") ? "All buckets" : k.buckets.join(", ")}</p>
                </td>
                <td className={`${td} hidden text-zinc-500 md:table-cell`}>{timeAgo(k.lastUsedAt)}</td>
                <td className={`${td} hidden text-zinc-500 lg:table-cell`}>{k.expiresAt ? formatDate(k.expiresAt) : "Never"}</td>
                <td className={td}>
                  <IconButton icon={Trash2} label="Revoke key" onClick={() => setDeleting(k)} />
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <CreateKeyDialog open={creating} onClose={() => setCreating(false)} />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting)}
        loading={del.isPending}
        title={`Revoke ${deleting?.name}?`}
        confirmLabel="Revoke"
      >
        Applications using this key will immediately lose access.
      </ConfirmDialog>
    </div>
  );
}

function CreateKeyDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const me = useSession();
  const can = useCan();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [permission, setPermission] = useState("readwrite");
  const [allBuckets, setAllBuckets] = useState(true);
  const [buckets, setBuckets] = useState<string[]>([]);
  const [expiresDays, setExpiresDays] = useState("");
  const [userId, setUserId] = useState(me.id);
  const [created, setCreated] = useState<AccessKey & { secret: string }>();
  const bucketList = useQuery({ queryKey: ["buckets"], queryFn: listBuckets, enabled: open });
  const users = useQuery({ queryKey: ["users"], queryFn: listUsers, enabled: open && can.admin });
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings, enabled: open });

  const m = useMutation({
    mutationFn: createKey,
    onSuccess: (k) => {
      setCreated(k);
      queryClient.invalidateQueries({ queryKey: ["keys"] });
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};

  const close = () => {
    setCreated(undefined);
    setName("");
    m.reset();
    onClose();
  };

  function submit(e: FormEvent) {
    e.preventDefault();
    m.mutate({
      name,
      permission,
      buckets: allBuckets ? ["*"] : buckets,
      expiresAt: expiresDays ? new Date(Date.now() + Number(expiresDays) * 86400_000).toISOString() : undefined,
      userId: userId !== me.id ? userId : undefined,
    });
  }

  const endpoint = settings.data?.s3Endpoint ?? "";
  return (
    <Modal
      open={open}
      onClose={close}
      wide={!!created}
      title={created ? "Access key created" : "New access key"}
      footer={
        created ? (
          <Button onClick={close}>I've saved the secret</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" form="create-key" loading={m.isPending} disabled={!allBuckets && buckets.length === 0}>
              Create key
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-4">
          <Alert tone="warning">Copy the secret now. It will not be shown again.</Alert>
          <CopyField label="Access key ID" value={created.id} />
          <CopyField label="Secret access key" value={created.secret} />
          <CopyField label="S3 endpoint" value={endpoint} />
          <div>
            <p className="mb-1.5 text-sm font-medium">Quick start</p>
            <pre className="overflow-x-auto rounded-lg bg-zinc-950 p-3 text-xs leading-relaxed text-zinc-100">
              {`# AWS CLI
export AWS_ACCESS_KEY_ID=${created.id}
export AWS_SECRET_ACCESS_KEY=${created.secret}
aws --endpoint-url ${endpoint} s3 ls

# REST API
curl -H "Authorization: Bearer ${created.id}:${created.secret}" \\
  ${window.location.origin}/api/v1/buckets`}
            </pre>
          </div>
        </div>
      ) : (
        <form id="create-key" onSubmit={submit} className="space-y-4">
          {m.error && Object.keys(fields).length === 0 && <Alert>{m.error.message}</Alert>}
          <Field label="Name" value={name} onChange={(e) => setName(e.target.value)} error={fields.name} placeholder="e.g. backup-server" autoFocus required />
          {can.admin && users.data && (
            <Select label="Owner" value={userId} onChange={(e) => setUserId(Number(e.target.value))}>
              {users.data.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.username} ({u.role})
                </option>
              ))}
            </Select>
          )}
          <Select
            label="Permission"
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
            hint="Keys can never exceed their owner's role. Full access includes bucket and admin management."
          >
            <option value="read">Read only</option>
            <option value="readwrite">Read & write objects</option>
            <option value="full">Full access</option>
          </Select>
          <Toggle checked={allBuckets} onChange={setAllBuckets} label="All buckets" description="Turn off to limit this key to specific buckets." />
          {!allBuckets && (
            <div className="max-h-40 space-y-1 overflow-y-auto rounded-lg border border-zinc-200 p-2 dark:border-zinc-800">
              {bucketList.data?.length === 0 && <p className="p-1 text-sm text-zinc-500">No buckets yet.</p>}
              {bucketList.data?.map((b) => (
                <label key={b.name} className="flex items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-zinc-50 dark:hover:bg-zinc-800">
                  <input
                    type="checkbox"
                    className="accent-accent-600"
                    checked={buckets.includes(b.name)}
                    onChange={(e) => setBuckets(e.target.checked ? [...buckets, b.name] : buckets.filter((x) => x !== b.name))}
                  />
                  {b.name}
                </label>
              ))}
            </div>
          )}
          <Select label="Expires" value={expiresDays} onChange={(e) => setExpiresDays(e.target.value)}>
            <option value="">Never</option>
            <option value="7">In 7 days</option>
            <option value="30">In 30 days</option>
            <option value="90">In 90 days</option>
            <option value="365">In 1 year</option>
          </Select>
        </form>
      )}
    </Modal>
  );
}
