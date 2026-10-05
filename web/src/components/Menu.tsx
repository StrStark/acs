import { useEffect, useRef, useState, type ReactNode } from "react";
import { MoreHorizontal, type LucideIcon } from "lucide-react";

export type MenuItem = { label: string; icon?: LucideIcon; onClick: () => void; danger?: boolean; hidden?: boolean };

/** A small kebab menu; items with hidden=true are omitted. */
export function Menu({ items, trigger }: { items: MenuItem[]; trigger?: ReactNode }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !ref.current?.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  const visible = items.filter((i) => !i.hidden);
  if (visible.length === 0) return null;
  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        aria-label="More actions"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation();
          setOpen(!open);
        }}
        className="rounded-md p-1.5 text-zinc-500 transition hover:bg-zinc-100 hover:text-zinc-900 dark:hover:bg-zinc-800 dark:hover:text-zinc-100"
      >
        {trigger ?? <MoreHorizontal className="size-4" />}
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 z-30 mt-1 min-w-44 overflow-hidden rounded-xl border border-zinc-200 bg-white py-1 shadow-lg dark:border-zinc-700 dark:bg-zinc-900"
        >
          {visible.map(({ label, icon: Icon, onClick, danger }) => (
            <button
              key={label}
              role="menuitem"
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                setOpen(false);
                onClick();
              }}
              className={`flex w-full items-center gap-2.5 px-3 py-2 text-left text-sm transition hover:bg-zinc-100 dark:hover:bg-zinc-800 ${
                danger ? "text-red-600 dark:text-red-400" : ""
              }`}
            >
              {Icon && <Icon className="size-4" />}
              {label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
