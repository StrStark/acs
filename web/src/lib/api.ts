// Client for the /api/v1 management API. Errors are RFC 9457 problem+json.

export class ApiError extends Error {
  status: number;
  fields: Record<string, string>;

  constructor(status: number, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.status = status;
    this.fields = fields;
  }
}

async function parseError(res: Response): Promise<ApiError> {
  let detail = res.statusText;
  let fields: Record<string, string> = {};
  try {
    const problem = await res.json();
    detail = problem.detail ?? problem.title ?? detail;
    fields = problem.errors ?? {};
  } catch {
    // Non-JSON error body; keep the status text.
  }
  return new ApiError(res.status, detail, fields);
}

export async function api<T>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method: init.method ?? "GET",
    credentials: "same-origin",
    headers: init.body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
  });
  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

/** Encodes an object key for use in a URL path, keeping slashes. */
export const encodeKey = (key: string) => key.split("/").map(encodeURIComponent).join("/");

export const qs = (params: Record<string, string | number | boolean | undefined | null>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  const s = p.toString();
  return s ? `?${s}` : "";
};

// ---- types ----

export type Role = "viewer" | "editor" | "admin";

export type User = { id: number; username: string; role: Role; createdAt: string };
export type Me = User & { key?: AccessKey };
export type SetupStatus = { needsSetup: boolean; tokenRequired: boolean };

export type Stats = { objects: number; bytes: number; storedBytes: number };
export type LifecycleRule = {
  id: string;
  enabled: boolean;
  prefix?: string;
  expirationDays?: number;
  noncurrentDays?: number;
  abortMultipartDays?: number;
};
export type CORSRule = {
  allowedOrigins: string[];
  allowedMethods: string[];
  allowedHeaders?: string[];
  exposeHeaders?: string[];
  maxAgeSeconds?: number;
};
export type Bucket = {
  name: string;
  createdAt: string;
  versioning?: "" | "Enabled" | "Suspended";
  public?: boolean;
  quotaBytes?: number;
  quotaObjects?: number;
  // Omitted by the server when empty.
  lifecycle?: LifecycleRule[];
  cors?: CORSRule[];
  stats: Stats;
};

export type ObjectInfo = {
  key: string;
  size: number;
  etag?: string;
  contentType?: string;
  lastModified: string;
  versionId?: string;
  isLatest: boolean;
  deleteMarker?: boolean;
  metadata?: Record<string, string>;
  headers?: Record<string, string>;
  tags?: Record<string, string>;
};
export type Listing = { objects: ObjectInfo[]; prefixes: string[]; nextCursor?: string };

export type AccessKey = {
  id: string;
  userId: number;
  username: string;
  name: string;
  permission: "read" | "readwrite" | "full";
  buckets: string[];
  createdAt: string;
  expiresAt?: string;
  lastUsedAt?: string;
};

export type ShareType = "file" | "folder" | "upload";
export type Share = {
  id: number;
  token: string;
  type: ShareType;
  bucket: string;
  key: string;
  createdBy?: number;
  createdByName?: string;
  createdAt: string;
  expiresAt?: string;
  hasPassword: boolean;
  maxDownloads?: number;
  downloads: number;
  views: number;
  maxUploadBytes?: number;
  note: string;
  disabled: boolean;
  lastAccessedAt?: string;
  url: string;
};

export type Settings = {
  siteName: string;
  publicUrl: string;
  s3PublicUrl: string;
  region: string;
  s3Endpoint: string;
  s3Enabled: boolean;
};

export type AuditEntry = {
  id: number;
  time: string;
  actor: string;
  action: string;
  target: string;
  ip: string;
  detail?: Record<string, unknown>;
};

export type Webhook = {
  id: number;
  name: string;
  url: string;
  secret: string;
  events: string[];
  bucket: string;
  prefix: string;
  enabled: boolean;
  createdAt: string;
};
export type Delivery = {
  id: number;
  webhookId: number;
  event: string;
  payload: string;
  status: number;
  error: string;
  attempt: number;
  durationMs: number;
  createdAt: string;
};

export type SystemInfo = {
  version: string;
  goVersion: string;
  startedAt: string;
  uptimeSeconds: number;
  dataDir?: string;
  disk: { total: number; used: number; free: number };
  totals: { buckets: number; objects: number; bytes: number; storedBytes: number };
};

// ---- auth ----

export const getSetupStatus = () => api<SetupStatus>("/setup");

/** Returns the signed-in user, or null when there is no valid session. */
export async function getMe(): Promise<Me | null> {
  try {
    return await api<Me>("/auth/me");
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) return null;
    throw e;
  }
}

export const runSetup = (body: { username: string; password: string; setupToken?: string }) =>
  api<User>("/setup", { method: "POST", body });
export const login = (body: { username: string; password: string }) =>
  api<User>("/auth/login", { method: "POST", body });
export const logout = () => api<void>("/auth/logout", { method: "POST", body: {} });
export const changePassword = (body: { currentPassword: string; newPassword: string }) =>
  api<void>("/auth/password", { method: "POST", body });

// ---- buckets & objects ----

export const listBuckets = () => api<Bucket[]>("/buckets");
export const getBucket = (name: string) => api<Bucket>(`/buckets/${name}`);
export const createBucket = (body: { name: string; versioning: boolean }) =>
  api<Bucket>("/buckets", { method: "POST", body });
export const updateBucket = (name: string, body: Partial<Omit<Bucket, "name" | "createdAt" | "stats">>) =>
  api<Bucket>(`/buckets/${name}`, { method: "PATCH", body });
export const deleteBucket = (name: string, force: boolean) =>
  api<void>(`/buckets/${name}${qs({ force })}`, { method: "DELETE" });

export const listObjects = (bucket: string, p: { prefix?: string; delimiter?: string; cursor?: string; limit?: number }) =>
  api<Listing>(`/buckets/${bucket}/objects${qs(p)}`);
export const objectInfo = (bucket: string, key: string, versionId?: string) =>
  api<ObjectInfo>(`/buckets/${bucket}/meta/${encodeKey(key)}${qs({ versionId })}`);
export const updateObject = (
  bucket: string,
  key: string,
  body: { contentType?: string; metadata?: Record<string, string>; tags?: Record<string, string> },
) => api<ObjectInfo>(`/buckets/${bucket}/meta/${encodeKey(key)}`, { method: "PATCH", body });
export const objectVersions = (bucket: string, key: string) =>
  api<ObjectInfo[]>(`/buckets/${bucket}/versions/${encodeKey(key)}`);
export const deleteObject = (bucket: string, key: string, versionId?: string) =>
  api<void>(`/buckets/${bucket}/objects/${encodeKey(key)}${qs({ versionId })}`, { method: "DELETE" });
export const bulkDelete = (bucket: string, body: { keys?: string[]; prefixes?: string[] }) =>
  api<{ deleted: number; errors: { key: string; error: string }[] }>(`/buckets/${bucket}/delete`, { method: "POST", body });
export const copyObject = (
  bucket: string,
  body: { sourceBucket?: string; sourceKey: string; sourceVersionId?: string; key: string; move?: boolean },
) => api<{ count: number }>(`/buckets/${bucket}/copy`, { method: "POST", body });
export const createFolder = (bucket: string, prefix: string) =>
  api<ObjectInfo>(`/buckets/${bucket}/folders`, { method: "POST", body: { prefix } });
export const restoreVersion = (bucket: string, key: string, versionId: string) =>
  api<ObjectInfo>(`/buckets/${bucket}/restore`, { method: "POST", body: { key, versionId } });

export const objectUrl = (bucket: string, key: string, opts: { download?: boolean; versionId?: string } = {}) =>
  `/api/v1/buckets/${bucket}/objects/${encodeKey(key)}${qs({ download: opts.download ? 1 : undefined, versionId: opts.versionId })}`;
export const zipUrl = (bucket: string, opts: { prefix?: string; keys?: string[] }) => {
  const p = new URLSearchParams();
  if (opts.prefix) p.set("prefix", opts.prefix);
  for (const k of opts.keys ?? []) p.append("key", k);
  return `/api/v1/buckets/${bucket}/zip?${p}`;
};

// ---- keys, users ----

export const listKeys = (all = false) => api<AccessKey[]>(`/keys${qs({ all: all || undefined })}`);
export const createKey = (body: {
  name: string;
  permission: string;
  buckets: string[];
  expiresAt?: string;
  userId?: number;
}) => api<AccessKey & { secret: string }>("/keys", { method: "POST", body });
export const deleteKey = (id: string) => api<void>(`/keys/${id}`, { method: "DELETE" });

export const listUsers = () => api<User[]>("/users");
export const createUser = (body: { username: string; password: string; role: Role }) =>
  api<User>("/users", { method: "POST", body });
export const updateUser = (id: number, body: { role?: Role; password?: string }) =>
  api<User>(`/users/${id}`, { method: "PATCH", body });
export const deleteUser = (id: number) => api<void>(`/users/${id}`, { method: "DELETE" });

// ---- shares ----

export const listShares = (p: { bucket?: string; key?: string } = {}) => api<Share[]>(`/shares${qs(p)}`);
export const createShare = (body: {
  type: ShareType;
  bucket: string;
  key: string;
  expiresAt?: string;
  password?: string;
  maxDownloads?: number;
  maxUploadBytes?: number;
  note?: string;
}) => api<Share>("/shares", { method: "POST", body });
export const updateShare = (id: number, body: Record<string, unknown>) =>
  api<Share>(`/shares/${id}`, { method: "PATCH", body });
export const deleteShare = (id: number) => api<void>(`/shares/${id}`, { method: "DELETE" });

// ---- admin ----

export const getSettings = () => api<Settings>("/settings");
export const updateSettings = (body: Pick<Settings, "siteName" | "publicUrl" | "s3PublicUrl" | "region">) =>
  api<Settings>("/settings", { method: "PUT", body });
export const listAudit = (p: { search?: string; before?: number; limit?: number }) =>
  api<AuditEntry[]>(`/audit${qs(p)}`);
export const listWebhooks = () => api<Webhook[]>("/webhooks");
export const webhookEvents = () => api<string[]>("/webhooks/events");
export type WebhookInput = Pick<Webhook, "name" | "url" | "events" | "bucket" | "prefix" | "enabled">;
export const createWebhook = (body: WebhookInput) => api<Webhook>("/webhooks", { method: "POST", body });
export const updateWebhook = (id: number, body: WebhookInput) =>
  api<Webhook>(`/webhooks/${id}`, { method: "PUT", body });
export const deleteWebhook = (id: number) => api<void>(`/webhooks/${id}`, { method: "DELETE" });
export const testWebhook = (id: number) => api<Delivery>(`/webhooks/${id}/test`, { method: "POST", body: {} });
export const webhookDeliveries = (id: number) => api<Delivery[]>(`/webhooks/${id}/deliveries`);
export const getSystemInfo = () => api<SystemInfo>("/system");

// ---- public shares ----

export type PublicShare = {
  type: ShareType;
  name: string;
  siteName: string;
  note: string;
  expiresAt?: string;
  requiresPassword: boolean;
  unlocked: boolean;
  downloadsLeft?: number;
  maxUploadBytes?: number;
  file?: { name: string; size: number; contentType: string; lastModified: string };
};
export type PublicListing = {
  path: string;
  files: { name: string; path: string; size: number; contentType: string; lastModified: string }[];
  folders: string[];
  nextCursor?: string;
};
const pub = (token: string) => `/public/shares/${encodeURIComponent(token)}`;
export const getPublicShare = (token: string) => api<PublicShare>(pub(token));
export const unlockShare = (token: string, password: string) =>
  api<void>(`${pub(token)}/unlock`, { method: "POST", body: { password } });
export const listPublicShare = (token: string, path: string, cursor?: string) =>
  api<PublicListing>(`${pub(token)}/list${qs({ path, cursor })}`);
export const publicDownloadUrl = (token: string, opts: { path?: string; download?: boolean } = {}) =>
  `/api/v1${pub(token)}/download${qs({ path: opts.path, download: opts.download ? 1 : undefined })}`;
export const publicZipUrl = (token: string) => `/api/v1${pub(token)}/zip`;
export const publicUploadUrl = (token: string, name: string) => `/api/v1${pub(token)}/upload/${encodeKey(name)}`;
