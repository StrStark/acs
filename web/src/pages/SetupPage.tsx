import { useState, type FormEvent } from "react";
import { Navigate, useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, runSetup, type SetupStatus } from "../lib/api";
import { Alert, AuthShell, Button, Field } from "../components/ui";

export function SetupPage({ status }: { status: SetupStatus }) {
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

  if (!status.needsSetup) return <Navigate to="/login" replace />;

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
    <AuthShell title="Welcome to ACS" subtitle="Create the administrator account to finish setting up your storage server.">
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
        <Field
          label="Username"
          name="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          error={fields.username}
          autoComplete="username"
          required
        />
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
    </AuthShell>
  );
}
