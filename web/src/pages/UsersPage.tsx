import { useState, type FormEvent } from "react";
import { Navigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { ApiError, createUser, deleteUser, listUsers, updateUser, type Role, type User } from "../lib/api";
import { formatDate } from "../lib/format";
import { useCan, useSession } from "../lib/session";
import { Menu } from "../components/Menu";
import { Alert, Badge, Button, ConfirmDialog, Field, Modal, PageHeader, Select, Spinner, Table, td, th, useToast } from "../components/ui";

const roleInfo: Record<Role, string> = {
  viewer: "Can browse and download.",
  editor: "Can upload, delete, share and manage buckets.",
  admin: "Full control, including users and server settings.",
};

export function UsersPage() {
  const can = useCan();
  const me = useSession();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [resetting, setResetting] = useState<User | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const users = useQuery({ queryKey: ["users"], queryFn: listUsers, enabled: can.admin });
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["users"] });

  const setRole = useMutation({
    mutationFn: ({ id, role }: { id: number; role: Role }) => updateUser(id, { role }),
    onSuccess: () => {
      toast("Role updated");
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });
  const del = useMutation({
    mutationFn: (u: User) => deleteUser(u.id),
    onSuccess: () => {
      toast("User deleted");
      setDeleting(null);
      refresh();
    },
    onError: (e) => toast(e.message, "error"),
  });

  if (!can.admin) return <Navigate to="/" replace />;

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader title="Users" description="People who can sign in to this panel." actions={<Button icon={Plus} onClick={() => setCreating(true)}>Add user</Button>} />
      {users.error && <Alert>{users.error.message}</Alert>}
      {users.isPending ? (
        <Spinner />
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>User</th>
              <th className={th}>Role</th>
              <th className={`${th} hidden md:table-cell`}>Created</th>
              <th className="w-12" />
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {users.data?.map((u) => (
              <tr key={u.id}>
                <td className={td}>
                  <div className="flex items-center gap-3">
                    <div className="grid size-8 place-items-center rounded-full bg-zinc-200 text-xs font-semibold uppercase dark:bg-zinc-800">{u.username.slice(0, 2)}</div>
                    <span className="font-medium">{u.username}</span>
                    {u.id === me.id && <Badge>You</Badge>}
                  </div>
                </td>
                <td className={td}>
                  <select
                    aria-label={`Role of ${u.username}`}
                    value={u.role}
                    disabled={u.id === me.id}
                    onChange={(e) => setRole.mutate({ id: u.id, role: e.target.value as Role })}
                    className="rounded-md border border-zinc-300 bg-white px-2 py-1 text-sm disabled:opacity-60 dark:border-zinc-700 dark:bg-zinc-900"
                  >
                    <option value="viewer">Viewer</option>
                    <option value="editor">Editor</option>
                    <option value="admin">Admin</option>
                  </select>
                </td>
                <td className={`${td} hidden text-zinc-500 md:table-cell`}>{formatDate(u.createdAt)}</td>
                <td className={td}>
                  <Menu
                    items={[
                      { label: "Reset password", icon: KeyRound, onClick: () => setResetting(u) },
                      { label: "Delete", icon: Trash2, danger: true, hidden: u.id === me.id, onClick: () => setDeleting(u) },
                    ]}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <CreateUserDialog open={creating} onClose={() => setCreating(false)} onDone={refresh} />
      <ResetPasswordDialog user={resetting} onClose={() => setResetting(null)} />
      <ConfirmDialog open={deleting !== null} onClose={() => setDeleting(null)} onConfirm={() => deleting && del.mutate(deleting)} loading={del.isPending} title={`Delete ${deleting?.username}?`}>
        Their access keys are revoked. Files they uploaded stay in place.
      </ConfirmDialog>
    </div>
  );
}

function CreateUserDialog({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("editor");
  const m = useMutation({
    mutationFn: createUser,
    onSuccess: (u) => {
      toast(`Created ${u.username}`);
      setUsername("");
      setPassword("");
      onClose();
      onDone();
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};
  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Add user"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="create-user" loading={m.isPending}>
            Add user
          </Button>
        </>
      }
    >
      <form
        id="create-user"
        className="space-y-4"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate({ username, password, role });
        }}
      >
        {m.error && Object.keys(fields).length === 0 && <Alert>{m.error.message}</Alert>}
        <Field label="Username" value={username} onChange={(e) => setUsername(e.target.value)} error={fields.username} autoFocus required />
        <Field label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} error={fields.password} hint="At least 10 characters." autoComplete="new-password" required />
        <Select label="Role" value={role} onChange={(e) => setRole(e.target.value as Role)} hint={roleInfo[role]}>
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
          <option value="admin">Admin</option>
        </Select>
      </form>
    </Modal>
  );
}

function ResetPasswordDialog({ user, onClose }: { user: User | null; onClose: () => void }) {
  const toast = useToast();
  const [password, setPassword] = useState("");
  const m = useMutation({
    mutationFn: () => updateUser(user!.id, { password }),
    onSuccess: () => {
      toast("Password reset; the user was signed out everywhere");
      setPassword("");
      onClose();
    },
  });
  const fields = m.error instanceof ApiError ? m.error.fields : {};
  return (
    <Modal
      open={user !== null}
      onClose={onClose}
      title={`Reset password for ${user?.username}`}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="reset-pw" loading={m.isPending}>
            Reset password
          </Button>
        </>
      }
    >
      <form
        id="reset-pw"
        onSubmit={(e: FormEvent) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        {m.error && Object.keys(fields).length === 0 && <Alert>{m.error.message}</Alert>}
        <Field label="New password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} error={fields.password} autoComplete="new-password" autoFocus />
      </form>
    </Modal>
  );
}
