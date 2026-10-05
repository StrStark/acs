import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
} from "react";
import { createPortal } from "react-dom";
import { Check, Copy, Loader2, X, type LucideIcon } from "lucide-react";

export function Logo({ className = "size-8" }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={className} aria-hidden="true">
      <rect width="32" height="32" rx="8" className="fill-accent-600" />
      <path d="M8 11.5 16 7l8 4.5-8 4.5-8-4.5Z" fill="#fff" />
      <path
        d="m8 16 8 4.5 8-4.5M8 20.5l8 4.5 8-4.5"
        fill="none"
        stroke="#fff"
        strokeWidth="2"
        strokeLinejoin="round"
        opacity=".75"
      />
    </svg>
  );
}

const inputBase =
  "block w-full rounded-lg border bg-white px-3 py-2 text-sm shadow-xs outline-none transition placeholder:text-zinc-400 focus:ring-4 disabled:opacity-60 dark:bg-zinc-900";
const inputOk = "border-zinc-300 focus:border-accent-500 focus:ring-accent-500/15 dark:border-zinc-700";
const inputErr = "border-red-400 focus:border-red-500 focus:ring-red-500/15";

type FieldProps = InputHTMLAttributes<HTMLInputElement> & { label?: string; error?: string; hint?: ReactNode };

export function Field({ label, error, hint, id, className = "", ...props }: FieldProps) {
  const autoId = useId();
  const inputId = id ?? props.name ?? autoId;
  return (
    <div className={`space-y-1.5 ${className}`}>
      {label && (
        <label htmlFor={inputId} className="block text-sm font-medium text-zinc-700 dark:text-zinc-300">
          {label}
        </label>
      )}
      <input id={inputId} aria-invalid={error ? true : undefined} className={`${inputBase} ${error ? inputErr : inputOk}`} {...props} />
      {error ? (
        <p className="text-xs text-red-600 dark:text-red-400">{error}</p>
      ) : hint ? (
        <p className="text-xs text-zinc-500">{hint}</p>
      ) : null}
    </div>
  );
}

type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & { label?: string; hint?: ReactNode };

export function Select({ label, hint, id, className = "", children, ...props }: SelectProps) {
  const autoId = useId();
  const inputId = id ?? props.name ?? autoId;
  return (
    <div className={`space-y-1.5 ${className}`}>
      {label && (
        <label htmlFor={inputId} className="block text-sm font-medium text-zinc-700 dark:text-zinc-300">
          {label}
        </label>
      )}
      <select id={inputId} className={`${inputBase} ${inputOk} pr-8`} {...props}>
        {children}
      </select>
      {hint && <p className="text-xs text-zinc-500">{hint}</p>}
    </div>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  description,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  description?: ReactNode;
  disabled?: boolean;
}) {
  return (
    <label className={`flex items-start justify-between gap-4 ${disabled ? "opacity-60" : "cursor-pointer"}`}>
      <span>
        <span className="block text-sm font-medium">{label}</span>
        {description && <span className="mt-0.5 block text-xs text-zinc-500 dark:text-zinc-400">{description}</span>}
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={`relative mt-0.5 inline-flex h-5 w-9 shrink-0 rounded-full transition focus-visible:outline-none focus-visible:ring-4 focus-visible:ring-accent-500/30 ${
          checked ? "bg-accent-600" : "bg-zinc-300 dark:bg-zinc-700"
        }`}
      >
        <span
          className={`absolute top-0.5 size-4 rounded-full bg-white shadow transition-all ${checked ? "left-[18px]" : "left-0.5"}`}
        />
      </button>
    </label>
  );
}

type Variant = "primary" | "secondary" | "danger" | "ghost";
const variants: Record<Variant, string> = {
  primary: "bg-accent-600 text-white shadow-sm hover:bg-accent-700",
  secondary:
    "border border-zinc-300 bg-white text-zinc-800 shadow-xs hover:bg-zinc-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100 dark:hover:bg-zinc-800",
  danger: "bg-red-600 text-white shadow-sm hover:bg-red-700",
  ghost: "text-zinc-600 hover:bg-zinc-100 hover:text-zinc-900 dark:text-zinc-400 dark:hover:bg-zinc-800 dark:hover:text-zinc-100",
};

export function Button({
  loading,
  children,
  variant = "primary",
  size = "md",
  icon: Icon,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  loading?: boolean;
  variant?: Variant;
  size?: "sm" | "md";
  icon?: LucideIcon;
}) {
  const sz = size === "sm" ? "px-2.5 py-1.5 text-xs" : "px-3.5 py-2 text-sm";
  return (
    <button
      type="button"
      disabled={loading || props.disabled}
      className={`inline-flex items-center justify-center gap-2 rounded-lg font-medium transition focus-visible:outline-none focus-visible:ring-4 focus-visible:ring-accent-500/30 disabled:cursor-not-allowed disabled:opacity-60 ${sz} ${variants[variant]} ${className}`}
      {...props}
    >
      {loading ? <Loader2 className="size-4 animate-spin" /> : Icon ? <Icon className="size-4" /> : null}
      {children}
    </button>
  );
}

export function IconButton({
  icon: Icon,
  label,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { icon: LucideIcon; label: string }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      className={`rounded-md p-1.5 text-zinc-500 transition hover:bg-zinc-100 hover:text-zinc-900 disabled:opacity-40 dark:hover:bg-zinc-800 dark:hover:text-zinc-100 ${className}`}
      {...props}
    >
      <Icon className="size-4" />
    </button>
  );
}

export function Alert({ children, tone = "error" }: { children: ReactNode; tone?: "error" | "info" | "warning" }) {
  const tones = {
    error: "border-red-200 bg-red-50 text-red-700 dark:border-red-900/60 dark:bg-red-950/40 dark:text-red-300",
    info: "border-accent-100 bg-accent-50 text-accent-700 dark:border-accent-500/20 dark:bg-accent-500/10 dark:text-accent-400",
    warning: "border-amber-200 bg-amber-50 text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/40 dark:text-amber-300",
  };
  return (
    <div role={tone === "error" ? "alert" : "status"} className={`rounded-lg border px-3 py-2 text-sm ${tones[tone]}`}>
      {children}
    </div>
  );
}

export function Badge({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "accent" | "green" | "amber" | "red" }) {
  const tones = {
    neutral: "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
    accent: "bg-accent-50 text-accent-700 dark:bg-accent-500/10 dark:text-accent-400",
    green: "bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-400",
    amber: "bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-400",
    red: "bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-400",
  };
  return <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${tones[tone]}`}>{children}</span>;
}

export function Spinner({ className = "size-5" }: { className?: string }) {
  return <Loader2 className={`animate-spin text-zinc-400 ${className}`} />;
}

export function FullPageSpinner() {
  return (
    <div className="grid min-h-screen place-items-center">
      <Spinner className="size-6" />
    </div>
  );
}

export function Card({ children, className = "" }: { children: ReactNode; className?: string }) {
  return (
    <div className={`rounded-2xl border border-zinc-200 bg-white dark:border-zinc-800 dark:bg-zinc-900/60 ${className}`}>
      {children}
    </div>
  );
}

export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-4">
      <div className="min-w-0">
        <h1 className="truncate text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-zinc-500 dark:text-zinc-400">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function EmptyState({ icon: Icon, title, children, action }: { icon: LucideIcon; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="rounded-2xl border border-dashed border-zinc-300 px-6 py-14 text-center dark:border-zinc-700">
      <Icon className="mx-auto size-8 text-zinc-400" />
      <h2 className="mt-4 font-semibold">{title}</h2>
      {children && <p className="mx-auto mt-1 max-w-md text-sm text-zinc-500 dark:text-zinc-400">{children}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  wide,
}: {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) return null;
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-end justify-center p-0 sm:items-center sm:p-4">
      <div className="absolute inset-0 bg-zinc-950/40 backdrop-blur-[2px]" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        className={`relative flex max-h-[92vh] w-full flex-col rounded-t-2xl border border-zinc-200 bg-white shadow-xl sm:rounded-2xl dark:border-zinc-800 dark:bg-zinc-900 ${
          wide ? "sm:max-w-2xl" : "sm:max-w-md"
        }`}
      >
        <div className="flex items-center justify-between border-b border-zinc-200 px-5 py-3.5 dark:border-zinc-800">
          <h2 className="font-semibold">{title}</h2>
          <IconButton icon={X} label="Close" onClick={onClose} />
        </div>
        <div className="overflow-y-auto px-5 py-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-zinc-200 px-5 py-3 dark:border-zinc-800">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  children,
  confirmLabel = "Delete",
  loading,
  danger = true,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  children: ReactNode;
  confirmLabel?: string;
  loading?: boolean;
  danger?: boolean;
}) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant={danger ? "danger" : "primary"} loading={loading} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-zinc-600 dark:text-zinc-300">{children}</div>
    </Modal>
  );
}

export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <IconButton
      icon={done ? Check : Copy}
      label={done ? "Copied" : label}
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    />
  );
}

/** A read-only value with a copy button, e.g. secrets and URLs. */
export function CopyField({ label, value, mono = true }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="space-y-1.5">
      <span className="block text-sm font-medium text-zinc-700 dark:text-zinc-300">{label}</span>
      <div className="flex items-center gap-1 rounded-lg border border-zinc-300 bg-zinc-50 py-1 pl-3 pr-1 dark:border-zinc-700 dark:bg-zinc-950">
        <span className={`min-w-0 flex-1 truncate text-sm ${mono ? "font-mono" : ""}`} title={value}>
          {value}
        </span>
        <CopyButton value={value} />
      </div>
    </div>
  );
}

// ---- toasts ----

type Toast = { id: number; message: string; tone: "success" | "error" };
const ToastContext = createContext<(message: string, tone?: Toast["tone"]) => void>(() => {});

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const push = useCallback((message: string, tone: Toast["tone"] = "success") => {
    const id = next.current++;
    setToasts((t) => [...t, { id, message, tone }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4000);
  }, []);
  return (
    <ToastContext.Provider value={push}>
      {children}
      {createPortal(
        <div className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-80 flex-col gap-2">
          {toasts.map((t) => (
            <div
              key={t.id}
              role="status"
              className={`pointer-events-auto rounded-lg border px-4 py-3 text-sm shadow-lg ${
                t.tone === "error"
                  ? "border-red-200 bg-red-50 text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300"
                  : "border-zinc-200 bg-white text-zinc-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
              }`}
            >
              {t.message}
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

export const useToast = () => useContext(ToastContext);

/** Centered card used by the setup, login and public share screens. */
export function AuthShell({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center px-4 py-12">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo className="size-11" />
          <h1 className="mt-5 text-xl font-semibold tracking-tight">{title}</h1>
          <p className="mt-1.5 text-sm text-zinc-500 dark:text-zinc-400">{subtitle}</p>
        </div>
        <Card className="p-6 shadow-sm">{children}</Card>
      </div>
    </div>
  );
}

export function Table({ children }: { children: ReactNode }) {
  return (
    <Card className="overflow-hidden">
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">{children}</table>
      </div>
    </Card>
  );
}

export const th = "px-4 py-2.5 text-xs font-medium uppercase tracking-wide text-zinc-500 dark:text-zinc-400";
export const td = "px-4 py-3 align-middle";
