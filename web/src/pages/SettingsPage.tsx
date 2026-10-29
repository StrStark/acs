import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen } from "lucide-react";
import { ApiError, changePassword, getRecovery, getSettings, getSystemInfo, setRecovery, updateSettings, type Settings } from "../lib/api";
import { formatDuration } from "../lib/format";
import { useCan, useSession } from "../lib/session";
import { Alert, Badge, Button, Card, CopyField, Field, PageHeader, useToast } from "../components/ui";

export function SettingsPage() {
  const can = useCan();
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <PageHeader title="Settings" />
      <AccountSection />
      {can.admin && <ServerSection />}
      {can.admin && <RecoverySection />}
      <ApiSection />
    </div>
  );
}

function AccountSection() {
  const me = useSession();
  const toast = useToast();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const m = useMutation({
    mutationFn: () => changePassword({ currentPassword: current, newPassword: next }),
    onSuccess: () => {
      toast("Password changed; other sessions were signed out");
      setCurrent("");
      setNext("");
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};
  return (
    <Card className="p-5">
      <h2 className="font-semibold">Account</h2>
      <p className="mb-4 mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
        Signed in as <span className="font-medium text-zinc-900 dark:text-zinc-100">{me.username}</span> ({me.role})
      </p>
      <form
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        {m.error && Object.keys(fields).length === 0 && (
          <div className="sm:col-span-2">
            <Alert>{m.error.message}</Alert>
          </div>
        )}
        <Field label="Current password" type="password" value={current} onChange={(e) => setCurrent(e.target.value)} error={fields.currentPassword} autoComplete="current-password" required />
        <Field label="New password" type="password" value={next} onChange={(e) => setNext(e.target.value)} error={fields.password} hint="At least 10 characters." autoComplete="new-password" required />
        <div>
          <Button type="submit" loading={m.isPending}>
            Change password
          </Button>
        </div>
      </form>
    </Card>
  );
}

function ServerSection() {
  const toast = useToast();
  const queryClient = useQueryClient();
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const sys = useQuery({ queryKey: ["system"], queryFn: getSystemInfo });
  const [form, setForm] = useState<Pick<Settings, "siteName" | "publicUrl" | "s3PublicUrl" | "region">>();
  useEffect(() => {
    if (settings.data && !form) {
      const { siteName, publicUrl, s3PublicUrl, region } = settings.data;
      setForm({ siteName, publicUrl, s3PublicUrl, region });
    }
  }, [settings.data, form]);
  const m = useMutation({
    mutationFn: () => updateSettings(form!),
    onSuccess: (s) => {
      queryClient.setQueryData(["settings"], s);
      toast("Settings saved");
    },
  });
  if (!form) return null;
  return (
    <Card className="p-5">
      <h2 className="font-semibold">Server</h2>
      <p className="mb-4 mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
        {sys.data && `Version ${sys.data.version} · up ${formatDuration(sys.data.uptimeSeconds)} · data in ${sys.data.dataDir}`}
      </p>
      <form
        className="space-y-4"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        {m.error && <Alert>{m.error.message}</Alert>}
        <Field label="Site name" value={form.siteName} onChange={(e) => setForm({ ...form, siteName: e.target.value })} hint="Shown in the panel and on public share pages." />
        <Field
          label="Public panel URL"
          value={form.publicUrl}
          onChange={(e) => setForm({ ...form, publicUrl: e.target.value })}
          placeholder="https://files.example.com"
          hint="Used to build share links. Leave empty to use the address you are browsing from."
        />
        <Field
          label="Public S3 endpoint"
          value={form.s3PublicUrl}
          onChange={(e) => setForm({ ...form, s3PublicUrl: e.target.value })}
          placeholder="https://s3.example.com"
          hint="Shown to users when they connect S3 clients."
        />
        <Field label="Region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} hint="Reported to S3 clients (default us-east-1)." />
        <Button type="submit" loading={m.isPending}>
          Save settings
        </Button>
      </form>
    </Card>
  );
}

function RecoverySection() {
  const toast = useToast();
  const queryClient = useQueryClient();
  const status = useQuery({ queryKey: ["recovery"], queryFn: getRecovery });
  const [passphrase, setPassphrase] = useState("");
  const [confirm, setConfirm] = useState("");
  const [current, setCurrent] = useState("");
  const [mismatch, setMismatch] = useState(false);
  const m = useMutation({
    mutationFn: () => setRecovery({ passphrase, currentPassword: current }),
    onSuccess: () => {
      toast("Recovery passphrase saved to every drive and provider");
      setPassphrase("");
      setConfirm("");
      setCurrent("");
      queryClient.invalidateQueries({ queryKey: ["recovery"] });
      queryClient.invalidateQueries({ queryKey: ["alert-summary"] });
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};
  const configured = status.data?.configured;
  useEffect(() => {
    if (status.data && window.location.hash === "#recovery") document.getElementById("recovery")?.scrollIntoView({ behavior: "smooth" });
  }, [status.data]);
  return (
    <Card className="p-5">
      <h2 id="recovery" className="flex scroll-mt-8 items-center gap-2 font-semibold">
        Recovery {status.data && (configured ? <Badge tone="green">Passphrase set</Badge> : <Badge tone="amber">Not set</Badge>)}
      </h2>
      <p className="mb-4 mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
        ACS keeps a copy of its metadata (users, buckets, keys, settings) on every drive and S3 provider it uses, so no separate backup is needed: any one of them is enough to rebuild
        this server on a new machine. Secrets in that copy are locked with this passphrase. Store it somewhere safe; it cannot be recovered.
      </p>
      <form
        className="grid gap-4 sm:grid-cols-2"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          if (passphrase !== confirm) {
            setMismatch(true);
            return;
          }
          setMismatch(false);
          m.mutate();
        }}
      >
        {m.error && Object.keys(fields).length === 0 && (
          <div className="sm:col-span-2">
            <Alert>{m.error.message}</Alert>
          </div>
        )}
        <Field label={configured ? "New passphrase" : "Passphrase"} type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} error={fields.passphrase} hint="At least 12 characters. A few random words work well." autoComplete="new-password" required />
        <Field label="Confirm passphrase" type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} error={mismatch ? "Passphrases do not match" : undefined} autoComplete="new-password" required />
        <Field label="Your current password" type="password" value={current} onChange={(e) => setCurrent(e.target.value)} error={fields.currentPassword} autoComplete="current-password" required />
        <div className="flex items-end">
          <Button type="submit" loading={m.isPending}>
            {configured ? "Change passphrase" : "Set passphrase"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function ApiSection() {
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  return (
    <Card className="space-y-4 p-5">
      <div>
        <h2 className="font-semibold">API</h2>
        <p className="mt-0.5 text-sm text-zinc-500 dark:text-zinc-400">
          Integrate with the S3-compatible API or the REST API. Authenticate REST calls with <code>Authorization: Bearer KEY_ID:SECRET</code>.
        </p>
      </div>
      <CopyField label="REST API base URL" value={`${window.location.origin}/api/v1`} />
      {settings.data && <CopyField label="S3 endpoint" value={settings.data.s3Endpoint} />}
      <a href="/api/v1/openapi.yaml" target="_blank" rel="noopener" className="inline-flex items-center gap-2 text-sm font-medium text-accent-600 hover:underline dark:text-accent-400">
        <BookOpen className="size-4" />
        OpenAPI specification
      </a>
    </Card>
  );
}
