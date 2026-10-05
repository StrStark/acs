import { createContext, useContext } from "react";
import type { Me, Role } from "./api";

export const SessionContext = createContext<Me | null>(null);

export function useSession(): Me {
  const me = useContext(SessionContext);
  if (!me) throw new Error("useSession outside an authenticated layout");
  return me;
}

const rank: Record<Role, number> = { viewer: 0, editor: 1, admin: 2 };

/** Mirrors the server's role checks for showing or hiding UI. */
export function useCan() {
  const me = useSession();
  const r = rank[me.role];
  return { write: r >= 1, manageBuckets: r >= 1, admin: r >= 2 };
}
