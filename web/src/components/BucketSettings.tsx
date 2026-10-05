import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { deleteBucket, getSettings, updateBucket, type Bucket, type LifecycleRule } from "../lib/api";
import { formatBytes, parseSize } from "../lib/format";
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Card, ConfirmDialog, CopyField, Field, IconButton, Toggle, useToast } from "./ui";

export function BucketSettings({ bucket }: { bucket: Bucket }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const save = useMutation({
    mutationFn: (body: Parameters<typeof updateBucket>[1]) => updateBucket(bucket.name, body),
    onSuccess: (b) => {
      queryClient.setQueryData(["bucket", bucket.name], b);
      queryClient.invalidateQueries({ queryKey: ["buckets"] });
      toast("Bucket settings saved");
    },
    onError: (e) => toast(e.message, "error"),
  });

  return (
    <div className="max-w-3xl space-y-6">
      <Section title="Versioning" description="Keep previous versions when objects are overwritten or deleted.">
        <Toggle
          checked={bucket.versioning === "Enabled"}
          disabled={save.isPending}
          onChange={(on) => save.mutate({ versioning: on ? "Enabled" : "Suspended" })}
          label={bucket.versioning === "Enabled" ? "Enabled" : bucket.versioning === "Suspended" ? "Suspended" : "Off"}
          description={
            bucket.versioning
              ? "Once enabled, versioning can be suspended but not fully turned off. Existing versions are kept."
              : "Turn on to protect against accidental overwrites and deletes."
          }
        />
      </Section>

      <Section title="Public access" description="Allow anyone to read objects without credentials.">
        <Toggle
          checked={!!bucket.public}
          disabled={save.isPending}
          onChange={(on) => save.mutate({ public: on })}
          label="Public read"
          description="Objects can be fetched anonymously through the S3 endpoint. Listing and uploads still require credentials."
        />
        {bucket.public && settings.data && (
          <div className="mt-4">
            <CopyField label="Public URL prefix" value={`${settings.data.s3Endpoint}/${bucket.name}/`} />
          </div>
        )}
      </Section>

      <QuotaSection bucket={bucket} saving={save.isPending} onSave={(q) => save.mutate(q)} />
      <LifecycleSection bucket={bucket} saving={save.isPending} onSave={(lifecycle) => save.mutate({ lifecycle })} />
      <CorsSection bucket={bucket} saving={save.isPending} onSave={(cors) => save.mutate({ cors })} />
      <DangerZone bucket={bucket} />
    </div>
  );
}

function Section({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return (
    <Card className="p-5">
      <h3 className="font-semibold">{title}</h3>
      <p className="mb-4 mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">{description}</p>
      {children}
    </Card>
  );
}

function QuotaSection({ bucket, saving, onSave }: { bucket: Bucket; saving: boolean; onSave: (q: { quotaBytes: number; quotaObjects: number }) => void }) {
  const [bytes, setBytes] = useState(bucket.quotaBytes ? formatBytes(bucket.quotaBytes) : "");
  const [objects, setObjects] = useState(bucket.quotaObjects ? String(bucket.quotaObjects) : "");
  const parsed = bytes.trim() ? parseSize(bytes) : 0;
  return (
    <Section title="Quota" description="Reject uploads once the bucket reaches these limits. Leave empty for unlimited.">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Maximum size"
          value={bytes}
          onChange={(e) => setBytes(e.target.value)}
          placeholder="e.g. 50 GB"
          error={parsed === undefined ? "Use a size like 500 MB or 2 TB" : undefined}
          hint={`Currently using ${formatBytes(bucket.stats.storedBytes)} (including old versions)`}
        />
        <Field
          label="Maximum objects"
          type="number"
          min={0}
          value={objects}
          onChange={(e) => setObjects(e.target.value)}
          placeholder="unlimited"
          hint={`Currently ${bucket.stats.objects} objects`}
        />
      </div>
      <Button className="mt-4" loading={saving} disabled={parsed === undefined} onClick={() => onSave({ quotaBytes: parsed ?? 0, quotaObjects: Number(objects) || 0 })}>
        Save quota
      </Button>
    </Section>
  );
}

function LifecycleSection({ bucket, saving, onSave }: { bucket: Bucket; saving: boolean; onSave: (rules: LifecycleRule[]) => void }) {
  const [rules, setRules] = useState<LifecycleRule[]>(bucket.lifecycle ?? []);
  const set = (i: number, p: Partial<LifecycleRule>) => setRules(rules.map((r, j) => (j === i ? { ...r, ...p } : r)));
  const num = (v: string) => (v === "" ? undefined : Math.max(0, Number(v)));
  return (
    <Section title="Lifecycle rules" description="Automatically expire objects, old versions and abandoned uploads. Rules run hourly.">
      <div className="space-y-3">
        {rules.length === 0 && <p className="text-sm text-zinc-500">No rules.</p>}
        {rules.map((r, i) => (
          <div key={i} className="rounded-xl border border-zinc-200 p-4 dark:border-zinc-800">
            <div className="mb-3 flex items-center gap-3">
              <div className="flex-1">
                <Toggle checked={r.enabled} onChange={(v) => set(i, { enabled: v })} label={`Rule ${i + 1}`} />
              </div>
              <IconButton icon={Trash2} label="Remove rule" onClick={() => setRules(rules.filter((_, j) => j !== i))} />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Prefix" value={r.prefix ?? ""} onChange={(e) => set(i, { prefix: e.target.value })} placeholder="(whole bucket)" />
              <Field
                label="Delete objects after (days)"
                type="number"
                min={0}
                value={r.expirationDays ?? ""}
                onChange={(e) => set(i, { expirationDays: num(e.target.value) })}
              />
              <Field
                label="Delete old versions after (days)"
                type="number"
                min={0}
                value={r.noncurrentDays ?? ""}
                onChange={(e) => set(i, { noncurrentDays: num(e.target.value) })}
                hint="Counted from when a version stopped being current."
              />
              <Field
                label="Abort incomplete uploads after (days)"
                type="number"
                min={0}
                value={r.abortMultipartDays ?? ""}
                onChange={(e) => set(i, { abortMultipartDays: num(e.target.value) })}
              />
            </div>
          </div>
        ))}
      </div>
      <div className="mt-4 flex gap-2">
        <Button variant="secondary" icon={Plus} onClick={() => setRules([...rules, { id: "", enabled: true }])}>
          Add rule
        </Button>
        <Button loading={saving} onClick={() => onSave(rules)}>
          Save rules
        </Button>
      </div>
    </Section>
  );
}

function CorsSection({ bucket, saving, onSave }: { bucket: Bucket; saving: boolean; onSave: (cors: NonNullable<Bucket["cors"]>) => void }) {
  const [text, setText] = useState(JSON.stringify(bucket.cors ?? [], null, 2));
  const [error, setError] = useState<string>();
  const example = `[{"allowedOrigins":["https://app.example.com"],"allowedMethods":["GET","PUT"],"allowedHeaders":["*"],"maxAgeSeconds":3600}]`;
  return (
    <Section title="CORS" description="Allow browser apps on other origins to call the S3 endpoint for this bucket (e.g. direct uploads with presigned URLs).">
      {error && <Alert>{error}</Alert>}
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={6}
        spellCheck={false}
        aria-label="CORS rules (JSON)"
        className="mt-2 block w-full rounded-lg border border-zinc-300 bg-zinc-50 p-3 font-mono text-xs dark:border-zinc-700 dark:bg-zinc-950"
      />
      <p className="mt-1 text-xs text-zinc-500">
        Example: <code className="break-all">{example}</code>
      </p>
      <Button
        className="mt-3"
        loading={saving}
        onClick={() => {
          try {
            const v = JSON.parse(text || "[]");
            if (!Array.isArray(v)) throw new Error("must be a JSON array");
            setError(undefined);
            onSave(v);
          } catch (e) {
            setError(`Invalid JSON: ${(e as Error).message}`);
          }
        }}
      >
        Save CORS
      </Button>
    </Section>
  );
}

function DangerZone({ bucket }: { bucket: Bucket }) {
  const navigate = useNavigate();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [confirmName, setConfirmName] = useState("");
  const del = useMutation({
    mutationFn: () => deleteBucket(bucket.name, true),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["buckets"] });
      toast(`Deleted bucket ${bucket.name}`);
      navigate("/buckets");
    },
    onError: (e) => toast(e.message, "error"),
  });
  return (
    <Card className="border-red-200 p-5 dark:border-red-900/60">
      <h3 className="font-semibold text-red-700 dark:text-red-400">Delete bucket</h3>
      <p className="mb-4 mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
        Permanently deletes the bucket, every object and version in it, and all of its share links.
      </p>
      <Button variant="danger" icon={Trash2} onClick={() => setOpen(true)}>
        Delete bucket
      </Button>
      <ConfirmDialog
        open={open}
        onClose={() => setOpen(false)}
        onConfirm={() => confirmName === bucket.name && del.mutate()}
        loading={del.isPending}
        title={`Delete ${bucket.name}?`}
        confirmLabel="Delete permanently"
      >
        <p className="mb-3">
          This deletes {bucket.stats.objects} objects ({formatBytes(bucket.stats.storedBytes)}) and cannot be undone. Type the bucket name to confirm.
        </p>
        <Field value={confirmName} onChange={(e) => setConfirmName(e.target.value)} placeholder={bucket.name} aria-label="Bucket name" />
      </ConfirmDialog>
    </Card>
  );
}
