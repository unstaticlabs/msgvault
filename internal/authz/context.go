package authz

import "context"

type principalContextKey struct{}

// WithPrincipal carries an already authenticated principal across in-process
// adapters. It never establishes authority from a wire header.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	if principal.VisibleSourceIDs != nil {
		principal.VisibleSourceIDs = append([]int64{}, principal.VisibleSourceIDs...)
	}
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext returns the identity preserved by an in-process adapter.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
