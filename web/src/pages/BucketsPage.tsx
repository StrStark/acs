import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Globe, History, Plus } from "lucide-react";
import { ApiError, createBucket, listBuckets } from "../lib/api";
import { formatBytes, formatDate, formatNumber } from "../lib/format";
import { useCan } from "../lib/session";
import { Alert, Badge, Button, EmptyState, Field, Modal, PageHeader, Spinner, Table, Toggle, td, th } from "../components/ui";

export function BucketsPage() {
  const can = useCan();
  const [creating, setCreating] = useState(false);
  const { data, isPending, error } = useQuery({ queryKey: ["buckets"], queryFn: listBuckets });

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Buckets"
        description="Top-level containers for your objects."
        actions={
          can.manageBuckets && (
            <Button icon={Plus} onClick={() => setCreating(true)}>
              New bucket
            </Button>
          )
        }
      />
      {error && <Alert>{error.message}</Alert>}
      {isPending ? (
        <div className="grid place-items-center py-20">
          <Spinner />
        </div>
      ) : data && data.length === 0 ? (
        <EmptyState
          icon={Database}
          title="No buckets yet"
          action={can.manageBuckets && <Button icon={Plus} onClick={() => setCreating(true)}>Create bucket</Button>}
        >
          Buckets hold your files. Create one, then upload from the browser or any S3 client.
        </EmptyState>
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>Name</th>
              <th className={th}>Objects</th>
              <th className={th}>Size</th>
              <th className={`${th} hidden md:table-cell`}>Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {data?.map((b) => (
              <tr key={b.name} className="hover:bg-zinc-50 dark:hover:bg-zinc-800/40">
                <td className={td}>
                  <Link to={`/buckets/${b.name}`} className="flex items-center gap-2.5 font-medium hover:underline">
                    <Database className="size-4 text-accent-600" />
                    {b.name}
                  </Link>
                  <div className="mt-1 flex gap-1.5 pl-6.5">
                    {b.versioning === "Enabled" && (
                      <Badge tone="accent">
                        <History className="mr-1 size-3" />
                        Versioned
                      </Badge>
                    )}
                    {b.public && (
                      <Badge tone="amber">
                        <Globe className="mr-1 size-3" />
                        Public
                      </Badge>
                    )}
                  </div>
                </td>
                <td className={`${td} tabular-nums`}>{formatNumber(b.stats.objects)}</td>
                <td className={`${td} tabular-nums`}>
                  {formatBytes(b.stats.storedBytes)}
                  {b.quotaBytes ? <span className="text-zinc-500"> / {formatBytes(b.quotaBytes)}</span> : null}
                </td>
                <td className={`${td} hidden text-zinc-500 md:table-cell`}>{formatDate(b.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      <CreateBucketDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  );
}

function CreateBucketDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [versioning, setVersioning] = useState(false);
  const mutation = useMutation({
    mutationFn: createBucket,
    onSuccess: (b) => {
      queryClient.invalidateQueries({ queryKey: ["buckets"] });
      onClose();
      setName("");
      navigate(`/buckets/${b.name}`);
    },
  });
  const fields = mutation.error instanceof ApiError ? mutation.error.fields : {};

  function submit(e: FormEvent) {
    e.preventDefault();
    mutation.mutate({ name: name.trim(), versioning });
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New bucket"
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="create-bucket" loading={mutation.isPending}>
            Create bucket
          </Button>
        </>
      }
    >
      <form id="create-bucket" onSubmit={submit} className="space-y-5">
        {mutation.error && !fields.name && <Alert>{mutation.error.message}</Alert>}
        <Field
          label="Bucket name"
          name="name"
          value={name}
          onChange={(e) => setName(e.target.value.toLowerCase())}
          error={fields.name}
          hint="3–63 characters: lowercase letters, numbers, dots and hyphens."
          placeholder="my-bucket"
          autoFocus
          required
        />
        <Toggle
          checked={versioning}
          onChange={setVersioning}
          label="Versioning"
          description="Keep every version of each object so overwrites and deletes can be undone."
        />
      </form>
    </Modal>
  );
}
