package auth

import "slices"

// Roles, from least to most privileged.
const (
	RoleViewer = "viewer"
	RoleEditor = "editor"
	RoleAdmin  = "admin"
)

func ValidRole(r string) bool { return r == RoleViewer || r == RoleEditor || r == RoleAdmin }

// Access key permission levels.
const (
	KeyRead      = "read"
	KeyReadWrite = "readwrite"
	// KeyFull grants everything the owning user's role allows.
	KeyFull = "full"
)

func ValidKeyPermission(p string) bool { return p == KeyRead || p == KeyReadWrite || p == KeyFull }

type Action int

const (
	// ActRead lists and downloads objects.
	ActRead Action = iota
	// ActWrite uploads, modifies and deletes objects, and manages share links.
	ActWrite
	// ActManageBuckets creates, configures and deletes buckets.
	ActManageBuckets
	// ActAdmin manages users, settings, webhooks and the audit log.
	ActAdmin
)

// Principal is an authenticated caller: a user, optionally acting through an
// access key whose scope narrows what the user may do.
type Principal struct {
	User User
	// Key is set when the request was authenticated with an access key.
	Key *AccessKey
}

// Name identifies the caller in audit logs.
func (p Principal) Name() string {
	if p.Key != nil {
		return p.User.Username + " (key " + p.Key.ID + ")"
	}
	return p.User.Username
}

func roleAllows(role string, a Action) bool {
	switch role {
	case RoleAdmin:
		return true
	case RoleEditor:
		return a <= ActManageBuckets
	case RoleViewer:
		return a == ActRead
	}
	return false
}

// Can reports whether the principal may perform a on bucket ("" for actions
// not tied to a single bucket).
func (p Principal) Can(a Action, bucket string) bool {
	if !roleAllows(p.User.Role, a) {
		return false
	}
	if p.Key == nil {
		return true
	}
	switch p.Key.Permission {
	case KeyRead:
		if a != ActRead {
			return false
		}
	case KeyReadWrite:
		if a > ActWrite {
			return false
		}
	}
	return p.Key.AllowsBucket(bucket)
}

// AllBuckets reports whether the principal is not restricted to specific buckets.
func (p Principal) AllBuckets() bool {
	return p.Key == nil || slices.Contains(p.Key.Buckets, "*")
}
