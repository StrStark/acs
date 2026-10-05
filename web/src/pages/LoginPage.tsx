import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { login } from "../lib/api";
import { Alert, AuthShell, Button, Field } from "../components/ui";

export function LoginPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const mutation = useMutation({
    mutationFn: login,
    onSuccess: (user) => {
      queryClient.setQueryData(["me"], user);
      navigate("/", { replace: true });
    },
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    mutation.mutate({ username, password });
  }

  return (
    <AuthShell title="Sign in" subtitle="Sign in to manage your storage.">
      <form onSubmit={onSubmit} className="space-y-4">
        {mutation.error && <Alert>{mutation.error.message}</Alert>}
        <Field
          label="Username"
          name="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoFocus
          required
        />
        <Field
          label="Password"
          name="password"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
        <Button type="submit" className="w-full" loading={mutation.isPending}>
          Sign in
        </Button>
      </form>
    </AuthShell>
  );
}
