import { Link, useParams, useSearchParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { getBucket } from "../lib/api";
import { formatBytes, plural } from "../lib/format";
import { useCan } from "../lib/session";
import { FileBrowser } from "../components/FileBrowser";
import { BucketSettings } from "../components/BucketSettings";
import { Alert, Badge, Spinner } from "../components/ui";

export function BucketPage() {
  const params = useParams();
  const name = params.bucket!;
  // The splat is the current folder; decode each segment back into a key prefix.
  const splat = params["*"] ?? "";
  const prefix = splat ? splat.split("/").map(decodeURIComponent).join("/") : "";
  const [search, setSearch] = useSearchParams();
  const tab = search.get("tab") === "settings" ? "settings" : "files";
  const can = useCan();
  const bucket = useQuery({ queryKey: ["bucket", name], queryFn: () => getBucket(name) });

  if (bucket.isPending)
    return (
      <div className="grid place-items-center py-20">
        <Spinner />
      </div>
    );
  if (bucket.error)
    return (
      <div className="mx-auto max-w-6xl space-y-3">
        <Alert>{bucket.error.message}</Alert>
        <Link to="/buckets" className="text-sm text-accent-600 hover:underline">
          Back to buckets
        </Link>
      </div>
    );
  const b = bucket.data;

  return (
    <div className="mx-auto max-w-6xl">
      <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
        <div>
          <p className="text-sm text-zinc-500">
            <Link to="/buckets" className="hover:underline">
              Buckets
            </Link>
          </p>
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            {b.name}
            {b.versioning === "Enabled" && <Badge tone="accent">Versioned</Badge>}
            {b.public && <Badge tone="amber">Public</Badge>}
          </h1>
          <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">
            {plural(b.stats.objects, "object")} · {formatBytes(b.stats.storedBytes)}
            {b.quotaBytes ? ` of ${formatBytes(b.quotaBytes)}` : ""}
          </p>
        </div>
        {can.manageBuckets && (
          <div className="flex rounded-lg border border-zinc-200 p-0.5 text-sm dark:border-zinc-800">
            {(["files", "settings"] as const).map((t) => (
              <button
                key={t}
                type="button"
                onClick={() => setSearch(t === "files" ? {} : { tab: t })}
                className={`rounded-md px-3 py-1.5 font-medium capitalize transition ${
                  tab === t ? "bg-zinc-100 dark:bg-zinc-800" : "text-zinc-500 hover:text-zinc-900 dark:hover:text-zinc-100"
                }`}
              >
                {t}
              </button>
            ))}
          </div>
        )}
      </div>
      {tab === "settings" && can.manageBuckets ? <BucketSettings bucket={b} /> : <FileBrowser bucket={b} prefix={prefix} />}
    </div>
  );
}
