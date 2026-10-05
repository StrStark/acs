import { useState } from "react";
import { Navigate } from "react-router";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { listAudit } from "../lib/api";
import { formatDate } from "../lib/format";
import { useCan } from "../lib/session";
import { Alert, Badge, Button, PageHeader, Spinner, Table, td, th } from "../components/ui";

const PAGE = 50;

function tone(action: string) {
  if (action.endsWith("failed")) return "red" as const;
  if (action.includes("delete")) return "amber" as const;
  if (action.startsWith("auth.")) return "accent" as const;
  return "neutral" as const;
}

export function AuditPage() {
  const can = useCan();
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const log = useInfiniteQuery({
    queryKey: ["audit", query],
    queryFn: ({ pageParam }) => listAudit({ search: query, before: pageParam, limit: PAGE }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => (last.length === PAGE ? last[last.length - 1].id : undefined),
    enabled: can.admin,
  });
  if (!can.admin) return <Navigate to="/" replace />;
  const entries = log.data?.pages.flat() ?? [];

  return (
    <div className="mx-auto max-w-6xl">
      <PageHeader
        title="Audit log"
        description="Sign-ins and administrative changes."
        actions={
          <form
            className="relative"
            onSubmit={(e) => {
              e.preventDefault();
              setQuery(search);
            }}
          >
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-zinc-400" />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search actor, action, target"
              aria-label="Search audit log"
              className="w-64 rounded-lg border border-zinc-300 bg-white py-2 pl-8 pr-3 text-sm outline-none focus:border-accent-500 focus:ring-4 focus:ring-accent-500/15 dark:border-zinc-700 dark:bg-zinc-900"
            />
          </form>
        }
      />
      {log.error && <Alert>{log.error.message}</Alert>}
      {log.isPending ? (
        <Spinner />
      ) : (
        <Table>
          <thead className="border-b border-zinc-200 dark:border-zinc-800">
            <tr>
              <th className={th}>Time</th>
              <th className={th}>Actor</th>
              <th className={th}>Action</th>
              <th className={th}>Target</th>
              <th className={`${th} hidden lg:table-cell`}>IP</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-zinc-100 dark:divide-zinc-800">
            {entries.length === 0 && (
              <tr>
                <td colSpan={5} className="p-8 text-center text-zinc-500">
                  No entries.
                </td>
              </tr>
            )}
            {entries.map((e) => (
              <tr key={e.id} title={e.detail ? JSON.stringify(e.detail) : undefined}>
                <td className={`${td} whitespace-nowrap text-zinc-500`}>{formatDate(e.time)}</td>
                <td className={`${td} font-medium`}>{e.actor}</td>
                <td className={td}>
                  <Badge tone={tone(e.action)}>{e.action}</Badge>
                </td>
                <td className={`${td} max-w-xs truncate font-mono text-xs`}>{e.target}</td>
                <td className={`${td} hidden text-zinc-500 lg:table-cell`}>{e.ip}</td>
              </tr>
            ))}
          </tbody>
        </Table>
      )}
      {log.hasNextPage && (
        <div className="mt-4 text-center">
          <Button variant="secondary" loading={log.isFetchingNextPage} onClick={() => log.fetchNextPage()}>
            Load older entries
          </Button>
        </div>
      )}
    </div>
  );
}
