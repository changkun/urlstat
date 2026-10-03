package authkit

import "slices"

// HasScope returns true if the identity is permitted for the given scope.
// The admin parameter is the product-specific admin scope that implies all
// other scopes (e.g. "admin:sandbox" or "admin:topos"). IsSuperadmin and the
// admin scope both bypass fine-grained scope checks. The bearer-token dev
// identity is superadmin so local dev works uniformly.
//
// Each product can wrap this in a method on its own Identity alias:
//
//	func (i Identity) HasScope(scope string) bool {
//	    return authkit.HasScope(i, scope, "admin:myproduct")
//	}
func HasScope(id Identity, want, admin string) bool {
	if id.IsSuperadmin {
		return true
	}
	return slices.ContainsFunc(id.Scopes, func(s string) bool {
		return s == want || s == admin
	})
}
