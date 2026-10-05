import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen } from "lucide-react";
import { ApiError, changePassword, getSettings, getSystemInfo, updateSettings, type Settings } from "../lib/api";
import { formatDuration } from "../lib/format";
import { useCan, useSession } from "../lib/session";
import { Alert, Button, Card, CopyField, Field, PageHeader, useToast } from "../components/ui";

export function SettingsPage() {
  const can = useCan();
  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <PageHeader title="Settings" />
      <AccountSection />
      {can.admin && <ServerSection />}
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
