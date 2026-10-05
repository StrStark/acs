import { useState, type FormEvent } from "react";
import { Navigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Send, Trash2, Webhook as WebhookIcon } from "lucide-react";
import {
  ApiError,
  createWebhook,
  deleteWebhook,
  listWebhooks,
  testWebhook,
  updateWebhook,
  webhookDeliveries,
  webhookEvents,
  type Webhook,
  type WebhookInput,
} from "../lib/api";
import { timeAgo } from "../lib/format";
import { useCan } from "../lib/session";
import { Menu } from "../components/Menu";
import { Alert, Badge, Button, Card, ConfirmDialog, CopyField, EmptyState, Field, Modal, PageHeader, Spinner, Toggle, useToast } from "../components/ui";

export function WebhooksPage() {
  const can = useCan();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<Webhook | "new" | null>(null);
  const [deleting, setDeleting] = useState<Webhook | null>(null);
  const [viewing, setViewing] = useState<Webhook | null>(null);
  const hooks = useQuery({ queryKey: ["webhooks"], queryFn: listWebhooks, enabled: can.admin });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["webhooks"] });

  const test = useMutation({
    mutationFn: (h: Webhook) => testWebhook(h.id),
    onSuccess: (d) => {
      toast(d.error ? `Test failed: ${d.error}` : `Delivered (HTTP ${d.status}, ${d.durationMs} ms)`, d.error ? "error" : "success");
      queryClient.invalidateQueries({ queryKey: ["deliveries"] });
    },
    onError: (e) => toast(e.message, "error"),
  });
  const del = useMutation({
    mutationFn: (h: Webhook) => deleteWebhook(h.id),
    onSuccess: () => {
      setDeleting(null);
      refresh();
    },
  });

  if (!can.admin) return <Navigate to="/" replace />;

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title="Webhooks"
        description="Get an HTTP POST when objects, buckets or share links change."
        actions={<Button icon={Plus} onClick={() => setEditing("new")}>Add webhook</Button>}
      />
      {hooks.error && <Alert>{hooks.error.message}</Alert>}
      {hooks.isPending ? (
        <Spinner />
      ) : hooks.data?.length === 0 ? (
        <EmptyState icon={WebhookIcon} title="No webhooks" action={<Button icon={Plus} onClick={() => setEditing("new")}>Add webhook</Button>}>
          Deliveries are signed with HMAC-SHA256 (header X-ACS-Signature) and retried with backoff.
        </EmptyState>
      ) : (
        <div className="space-y-3">
          {hooks.data?.map((h) => (
            <Card key={h.id} className="flex items-center gap-4 p-4">
              <WebhookIcon className="size-5 shrink-0 text-accent-600" />
              <button type="button" className="min-w-0 flex-1 text-left" onClick={() => setViewing(h)}>
                <p className="flex items-center gap-2 font-medium">
                  {h.name} {!h.enabled && <Badge>Paused</Badge>}
                </p>
                <p className="truncate font-mono text-xs text-zinc-500">{h.url}</p>
                <p className="mt-1 text-xs text-zinc-500">
                  {h.events.includes("*") ? "All events" : h.events.join(", ")}
                  {h.bucket && ` · bucket ${h.bucket}`}
                  {h.prefix && ` · prefix ${h.prefix}`}
                </p>
              </button>
              <Button size="sm" variant="secondary" icon={Send} loading={test.isPending && test.variables?.id === h.id} onClick={() => test.mutate(h)}>
                Test
              </Button>
              <Menu
                items={[
                  { label: "Deliveries & secret", icon: Send, onClick: () => setViewing(h) },
                  { label: "Edit", icon: Pencil, onClick: () => setEditing(h) },
                  { label: "Delete", icon: Trash2, danger: true, onClick: () => setDeleting(h) },
                ]}
              />
            </Card>
          ))}
        </div>
      )}
      {editing && <WebhookDialog hook={editing === "new" ? null : editing} onClose={() => setEditing(null)} onDone={refresh} />}
      <DeliveriesDialog hook={viewing} onClose={() => setViewing(null)} />
      <ConfirmDialog open={deleting !== null} onClose={() => setDeleting(null)} onConfirm={() => deleting && del.mutate(deleting)} loading={del.isPending} title={`Delete ${deleting?.name}?`}>
        No further events will be sent to this endpoint.
      </ConfirmDialog>
    </div>
  );
}

function WebhookDialog({ hook, onClose, onDone }: { hook: Webhook | null; onClose: () => void; onDone: () => void }) {
  const events = useQuery({ queryKey: ["webhook-events"], queryFn: webhookEvents });
  const [form, setForm] = useState<WebhookInput>(
    hook
      ? { name: hook.name, url: hook.url, events: hook.events, bucket: hook.bucket, prefix: hook.prefix, enabled: hook.enabled }
      : { name: "", url: "", events: ["object.created", "object.deleted"], bucket: "", prefix: "", enabled: true },
  );
  const m = useMutation({
    mutationFn: () => (hook ? updateWebhook(hook.id, form) : createWebhook(form)),
    onSuccess: () => {
      onClose();
      onDone();
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};
  const all = form.events.includes("*");
  const toggleEvent = (e: string) =>
    setForm({ ...form, events: form.events.includes(e) ? form.events.filter((x) => x !== e) : [...form.events.filter((x) => x !== "*"), e] });

  return (
    <Modal
      open
      onClose={onClose}
      title={hook ? "Edit webhook" : "Add webhook"}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="webhook-form" loading={m.isPending}>
            Save
          </Button>
        </>
      }
    >
      <form
        id="webhook-form"
        className="space-y-4"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        {m.error && Object.keys(fields).length === 0 && <Alert>{m.error.message}</Alert>}
        <Field label="Name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} error={fields.name} required autoFocus />
        <Field label="Endpoint URL" type="url" value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} error={fields.url} placeholder="https://example.com/hooks/acs" required />
        <div>
          <p className="mb-2 text-sm font-medium text-zinc-700 dark:text-zinc-300">Events</p>
          <label className="mb-1 flex items-center gap-2 text-sm">
            <input type="checkbox" className="accent-accent-600" checked={all} onChange={() => setForm({ ...form, events: all ? [] : ["*"] })} />
            All events
          </label>
          {!all && (
            <div className="grid grid-cols-2 gap-1">
              {events.data?.map((e) => (
                <label key={e} className="flex items-center gap-2 font-mono text-xs">
                  <input type="checkbox" className="accent-accent-600" checked={form.events.includes(e)} onChange={() => toggleEvent(e)} />
                  {e}
                </label>
              ))}
            </div>
          )}
          {fields.events && <p className="mt-1 text-xs text-red-600">{fields.events}</p>}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Only bucket (optional)" value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} />
          <Field label="Only key prefix (optional)" value={form.prefix} onChange={(e) => setForm({ ...form, prefix: e.target.value })} />
        </div>
        <Toggle checked={form.enabled} onChange={(v) => setForm({ ...form, enabled: v })} label="Enabled" />
      </form>
    </Modal>
  );
}

function DeliveriesDialog({ hook, onClose }: { hook: Webhook | null; onClose: () => void }) {
  const deliveries = useQuery({
    queryKey: ["deliveries", hook?.id],
    queryFn: () => webhookDeliveries(hook!.id),
    enabled: hook !== null,
    refetchInterval: 5000,
  });
  const [expanded, setExpanded] = useState<number | null>(null);
  return (
    <Modal open={hook !== null} onClose={onClose} title={hook?.name ?? ""} wide>
      {hook && (
        <div className="space-y-5">
          <CopyField label="Signing secret" value={hook.secret} />
          <p className="text-xs text-zinc-500">
            Verify requests by computing HMAC-SHA256 over <code>{"<X-ACS-Timestamp>.<body>"}</code> with this secret and comparing it to{" "}
            <code>X-ACS-Signature</code> (<code>sha256=…</code>).
          </p>
          <div>
            <p className="mb-2 text-sm font-medium">Recent deliveries</p>
            {deliveries.data?.length === 0 && <p className="text-sm text-zinc-500">No deliveries yet. Use Test to send a ping.</p>}
            <ul className="divide-y divide-zinc-100 rounded-xl border border-zinc-200 dark:divide-zinc-800 dark:border-zinc-800">
              {deliveries.data?.map((d) => (
                <li key={d.id} className="px-3 py-2 text-sm">
                  <button type="button" className="flex w-full items-center gap-3 text-left" onClick={() => setExpanded(expanded === d.id ? null : d.id)}>
                    <Badge tone={d.error ? "red" : "green"}>{d.status || "ERR"}</Badge>
                    <span className="font-mono text-xs">{d.event}</span>
                    {d.attempt > 1 && <span className="text-xs text-zinc-500">attempt {d.attempt}</span>}
                    <span className="ml-auto text-xs text-zinc-500">
                      {d.durationMs} ms · {timeAgo(d.createdAt)}
                    </span>
                  </button>
                  {expanded === d.id && (
                    <div className="mt-2 space-y-2">
                      {d.error && <p className="text-xs text-red-600">{d.error}</p>}
                      <pre className="max-h-48 overflow-auto rounded-lg bg-zinc-950 p-2 text-xs text-zinc-100">
                        {JSON.stringify(JSON.parse(d.payload), null, 2)}
                      </pre>
                    </div>
                  )}
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </Modal>
  );
}
