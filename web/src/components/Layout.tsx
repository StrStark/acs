import { useState } from "react";
import { NavLink, Outlet, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Database,
  KeyRound,
  LayoutDashboard,
  Link2,
  LogOut,
  Menu,
  ScrollText,
  Settings,
  Users,
  Webhook,
  X,
  type LucideIcon,
} from "lucide-react";
import { getSettings, logout, type Me } from "../lib/api";
import { SessionContext } from "../lib/session";
import { Logo, IconButton } from "./ui";

type NavItem = { label: string; icon: LucideIcon; to: string; admin?: boolean; end?: boolean };

const nav: NavItem[] = [
  { label: "Dashboard", icon: LayoutDashboard, to: "/", end: true },
  { label: "Buckets", icon: Database, to: "/buckets" },
  { label: "Share links", icon: Link2, to: "/shares" },
  { label: "Access keys", icon: KeyRound, to: "/keys" },
  { label: "Users", icon: Users, to: "/users", admin: true },
  { label: "Webhooks", icon: Webhook, to: "/webhooks", admin: true },
  { label: "Audit log", icon: ScrollText, to: "/audit", admin: true },
  { label: "Settings", icon: Settings, to: "/settings" },
];

export function Layout({ user }: { user: Me }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [mobileOpen, setMobileOpen] = useState(false);
  const settings = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const siteName = settings.data?.siteName ?? "ACS Storage";
  const signOut = useMutation({
    mutationFn: logout,
    onSettled: () => {
      queryClient.clear();
      queryClient.setQueryData(["me"], null);
      navigate("/login", { replace: true });
    },
  });

  const sidebar = (
    <>
      <div className="flex h-16 items-center gap-2.5 px-5">
        <Logo className="size-7" />
        <span className="truncate font-semibold tracking-tight">{siteName}</span>
      </div>
      <nav className="flex-1 space-y-0.5 overflow-y-auto px-3 py-2">
        {nav
          .filter((n) => !n.admin || user.role === "admin")
          .map(({ label, icon: Icon, to, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              onClick={() => setMobileOpen(false)}
              className={({ isActive }) =>
                `flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition ${
                  isActive
                    ? "bg-accent-50 text-accent-700 dark:bg-accent-500/10 dark:text-accent-400"
                    : "text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
                }`
              }
            >
              <Icon className="size-4" />
              {label}
            </NavLink>
          ))}
      </nav>
      <div className="flex items-center gap-3 border-t border-zinc-200 p-4 dark:border-zinc-800">
        <div className="grid size-8 place-items-center rounded-full bg-zinc-200 text-xs font-semibold uppercase dark:bg-zinc-800">
          {user.username.slice(0, 2)}
        </div>
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{user.username}</p>
          <p className="text-xs capitalize text-zinc-500">{user.role}</p>
        </div>
        <IconButton icon={LogOut} label="Sign out" onClick={() => signOut.mutate()} />
      </div>
    </>
  );

  return (
    <SessionContext.Provider value={user}>
      <div className="flex min-h-screen">
        <aside className="sticky top-0 hidden h-screen w-60 shrink-0 flex-col border-r border-zinc-200 bg-white md:flex dark:border-zinc-800 dark:bg-zinc-900/40">
          {sidebar}
        </aside>

        {mobileOpen && (
          <div className="fixed inset-0 z-40 md:hidden">
            <div className="absolute inset-0 bg-zinc-950/40" onClick={() => setMobileOpen(false)} />
            <aside className="relative flex h-full w-64 flex-col bg-white dark:bg-zinc-900">
              <div className="absolute right-3 top-4">
                <IconButton icon={X} label="Close menu" onClick={() => setMobileOpen(false)} />
              </div>
              {sidebar}
            </aside>
          </div>
        )}

        <div className="flex min-w-0 flex-1 flex-col">
          <header className="flex h-14 items-center gap-3 border-b border-zinc-200 px-4 md:hidden dark:border-zinc-800">
            <IconButton icon={Menu} label="Open menu" onClick={() => setMobileOpen(true)} />
            <Logo className="size-6" />
            <span className="font-semibold">{siteName}</span>
          </header>
          <main className="flex-1 px-4 py-8 md:px-10">
            <Outlet />
          </main>
        </div>
      </div>
    </SessionContext.Provider>
  );
}
