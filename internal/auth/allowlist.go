// Package auth implements Telegram user authorization.
package auth

// Allowlist authorizes Telegram users by their numeric user ID.
// Usernames are intentionally not used for authorization because they
// can be changed by users at any time.
type Allowlist struct {
	ids map[int64]struct{}
}

// New creates an allowlist from the given user IDs.
func New(ids []int64) *Allowlist {
	a := &Allowlist{ids: make(map[int64]struct{}, len(ids))}
	for _, id := range ids {
		a.ids[id] = struct{}{}
	}
	return a
}

// Allowed reports whether the Telegram user ID is authorized.
func (a *Allowlist) Allowed(id int64) bool {
	_, ok := a.ids[id]
	return ok
}

// Size returns the number of authorized users.
func (a *Allowlist) Size() int {
	return len(a.ids)
}
