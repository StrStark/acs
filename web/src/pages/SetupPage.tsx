import { useEffect, useState, type FormEvent } from "react";
import { Navigate, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Cloud, HardDrive, History, UserPlus } from "lucide-react";
import { ApiError, getSetupStatus, restoreFromStorage, runSetup, type BackendInput, type RestoreCandidate, type SetupStatus } from "../lib/api";
import { plural, timeAgo } from "../lib/format";
import { Alert, AuthShell, Button, Field, Select, Spinner, Toggle } from "../components/ui";
import { providers } from "./StoragePage";

export function SetupPage({ status }: { status: SetupStatus }) {
  const restorable = status.restorable ?? [];
  // Offer restore first when this machine already has ACS data on a drive.
  const [mode, setMode] = useState<"create" | "restore">(restorable.length > 0 ? "restore" : "create");
  if (!status.needsSetup) return <Navigate to="/login" replace />;

  const tabs = (
    <div className="mb-5 grid grid-cols-2 gap-1 rounded-lg bg-zinc-100 p-1 text-sm font-medium dark:bg-zinc-800">
      {(
        [
          ["create", UserPlus, "New installation"],
          ["restore", History, "Restore"],
        ] as const
      ).map(([m, Icon, label]) => (
        <button
          key={m}
          type="button"
          onClick={() => setMode(m)}
          className={`flex items-center justify-center gap-2 rounded-md px-3 py-1.5 transition ${
            mode === m ? "bg-white shadow-sm dark:bg-zinc-900" : "text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100"
          }`}
        >
          <Icon className="size-4" />
          {label}
        </button>
      ))}
    </div>
  );

  return mode === "create" ? (
    <AuthShell title="Welcome to ACS" subtitle="Create the administrator account to finish setting up your storage server.">
      {tabs}
      {restorable.length > 0 && (
        <div className="mb-4">
          <Alert tone="warning">
            ACS data from <b>{restorable[0].siteName}</b> was found on an attached drive. Use <b>Restore</b> to bring it back instead.
          </Alert>
        </div>
      )}
      <CreateForm status={status} />
    </AuthShell>
  ) : (
    <AuthShell title="Restore ACS" subtitle="Every drive and S3 provider ACS uses carries a copy of its metadata. Restore users, keys, buckets and settings from one of them.">
      {tabs}
      <RestoreForm status={status} candidates={restorable} />
    </AuthShell>
  );
}

function CreateForm({ status }: { status: SetupStatus }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const [confirmError, setConfirmError] = useState<string>();

  const mutation = useMutation({
    mutationFn: runSetup,
    onSuccess: (user) => {
      queryClient.setQueryData(["setup"], { needsSetup: false, tokenRequired: false });
      queryClient.setQueryData(["me"], user);
      navigate("/", { replace: true });
    },
  });

  const fields = mutation.error instanceof ApiError ? mutation.error.fields : {};
  const formError = mutation.error && Object.keys(fields).length === 0 ? mutation.error.message : undefined;

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setConfirmError("Passwords do not match");
      return;
    }
    setConfirmError(undefined);
    mutation.mutate({ username, password, setupToken: status.tokenRequired ? setupToken : undefined });
  }

  return (
    <form onSubmit={onSubmit} className="space-y-4" noValidate>
      {formError && <Alert>{formError}</Alert>}
      {status.tokenRequired && (
        <Field
          label="Setup token"
          name="setupToken"
          value={setupToken}
          onChange={(e) => setSetupToken(e.target.value)}
          error={fields.setupToken}
          hint="The value of ACS_SETUP_TOKEN in your server configuration."
          autoComplete="off"
          required
        />
      )}
      <Field label="Username" name="username" value={username} onChange={(e) => setUsername(e.target.value)} error={fields.username} autoComplete="username" required />
      <Field
        label="Password"
        name="password"
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={fields.password}
        hint="At least 10 characters."
        autoComplete="new-password"
        autoFocus
        required
      />
      <Field
        label="Confirm password"
        name="confirm"
        type="password"
        value={confirm}
        onChange={(e) => setConfirm(e.target.value)}
        error={confirmError}
        autoComplete="new-password"
        required
      />
      <Button type="submit" className="w-full" loading={mutation.isPending}>
        Create account
      </Button>
    </form>
  );
}

function RestoreForm({ status, candidates }: { status: SetupStatus; candidates: RestoreCandidate[] }) {
  const queryClient = useQueryClient();
  const [source, setSource] = useState<string>(candidates[0]?.path ?? "s3");
  const [s3, setS3] = useState<BackendInput>({ name: "restore", type: "s3", provider: "arvan-simin", endpoint: providers[0].endpoint, pathStyle: true, bucket: "", prefix: "acs", accessKey: "", secretKey: "" });
  const [passphrase, setPassphrase] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const [restarting, setRestarting] = useState<string>();
  const chosen = candidates.find((c) => c.path === source);

  const mutation = useMutation({
    mutationFn: () =>
      restoreFromStorage({
        path: source === "s3" ? undefined : source,
        s3: source === "s3" ? s3 : undefined,
        passphrase,
        setupToken: status.tokenRequired ? setupToken : undefined,
      }),
    onSuccess: (r) => setRestarting(r.siteName),
  });

  // The server restarts with the restored data; wait until it reports that setup is done.
  useEffect(() => {
    if (!restarting) return;
    const t = setInterval(async () => {
      try {
        const st = await getSetupStatus();
        if (!st.needsSetup) {
          clearInterval(t);
          queryClient.setQueryData(["setup"], st);
          window.location.assign("/login");
        }
      } catch {
        // Still restarting.
      }
    }, 1500);
    return () => clearInterval(t);
  }, [restarting, queryClient]);

  if (restarting) {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <Spinner className="size-7" />
        <p className="font-medium">Restoring {restarting}…</p>
        <p className="text-sm text-zinc-500">ACS is restarting with the restored data. You can sign in with your existing account in a moment.</p>
      </div>
    );
  }

  const fields = mutation.error instanceof ApiError ? mutation.error.fields : {};
  const formError = mutation.error && Object.keys(fields).length === 0 ? mutation.error.message : undefined;
  const preset = providers.find((p) => p.id === s3.provider) ?? providers[providers.length - 1];
  const needsPassphrase = source === "s3" || chosen?.hasKeyring;

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      {formError && <Alert>{formError}</Alert>}
      <fieldset className="space-y-2">
        <legend className="mb-1.5 text-sm font-medium">Restore from</legend>
        {candidates.map((c) => (
          <SourceOption key={c.path} selected={source === c.path} onSelect={() => setSource(c.path)} icon={HardDrive} title={`${c.siteName} · ${c.backendName}`}>
            <span className="font-mono">{c.path}</span> · {plural(c.buckets?.length ?? 0, "bucket")} · synced {timeAgo(c.updatedAt)}
          </SourceOption>
        ))}
        <SourceOption selected={source === "s3"} onSelect={() => setSource("s3")} icon={Cloud} title="An S3 provider">
          ArvanCloud, ParsPack, AWS or MinIO storage this installation used
        </SourceOption>
        {candidates.length === 0 && (
          <p className="text-xs text-zinc-500">
            No ACS data was found on attached drives. Mount a drive ACS used under <code>/drives</code> and reload this page, or restore from an S3 provider.
          </p>
        )}
      </fieldset>

      {source === "s3" && (
        <div className="space-y-3 rounded-xl border border-zinc-200 p-3 dark:border-zinc-700">
          <Select
            label="Provider"
            value={s3.provider}
            onChange={(e) => {
              const p = providers.find((x) => x.id === e.target.value)!;
              setS3({ ...s3, provider: p.id, endpoint: p.endpoint, pathStyle: p.pathStyle, region: "region" in p ? p.region : "" });
            }}
          >
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </Select>
          <Field label="Endpoint" value={s3.endpoint ?? ""} onChange={(e) => setS3({ ...s3, endpoint: e.target.value })} error={fields.endpoint} placeholder={"placeholder" in preset ? preset.placeholder : ""} required />
          <div className="grid grid-cols-2 gap-3">
            <Field label="Bucket" value={s3.bucket ?? ""} onChange={(e) => setS3({ ...s3, bucket: e.target.value })} error={fields.bucket} required />
            <Field label="Prefix" value={s3.prefix ?? ""} onChange={(e) => setS3({ ...s3, prefix: e.target.value })} placeholder="acs" />
            <Field label="Access key" value={s3.accessKey ?? ""} onChange={(e) => setS3({ ...s3, accessKey: e.target.value })} error={fields.accessKey} autoComplete="off" required />
            <Field label="Secret key" type="password" value={s3.secretKey ?? ""} onChange={(e) => setS3({ ...s3, secretKey: e.target.value })} error={fields.secretKey} autoComplete="off" required />
          </div>
          <Toggle checked={!!s3.pathStyle} onChange={(v) => setS3({ ...s3, pathStyle: v })} label="Path-style requests" />
        </div>
      )}

      {status.tokenRequired && (
        <Field label="Setup token" value={setupToken} onChange={(e) => setSetupToken(e.target.value)} error={fields.setupToken} autoComplete="off" required />
      )}
      {needsPassphrase ? (
        <Field
          label="Recovery passphrase"
          type="password"
          value={passphrase}
          onChange={(e) => setPassphrase(e.target.value)}
          error={fields.passphrase}
          hint="Set under Settings → Recovery on the original server. It unlocks the stored access-key secrets and provider credentials."
          autoComplete="off"
          required
        />
      ) : (
        <Alert tone="warning">
          No recovery passphrase was set on this installation, so stored secrets cannot be recovered: S3 access keys and provider credentials will need to be re-created. Users, buckets
          and files are restored.
        </Alert>
      )}
      <Button type="submit" className="w-full" loading={mutation.isPending}>
        Restore
      </Button>
    </form>
  );
}

function SourceOption({
  selected,
  onSelect,
  icon: Icon,
  title,
  children,
}: {
  selected: boolean;
  onSelect: () => void;
  icon: typeof Cloud;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      className={`flex w-full items-start gap-3 rounded-xl border p-3 text-left transition ${
        selected ? "border-accent-500 bg-accent-50 dark:bg-accent-500/10" : "border-zinc-200 hover:border-zinc-300 dark:border-zinc-700"
      }`}
    >
      <Icon className="mt-0.5 size-4 shrink-0 text-accent-600" />
      <span className="min-w-0">
        <span className="block text-sm font-medium">{title}</span>
        <span className="block truncate text-xs text-zinc-500">{children}</span>
      </span>
    </button>
  );
}
