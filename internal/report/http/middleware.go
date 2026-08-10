// Package reporthttp owns Report V2 HTTP boundaries. The route handlers are
// introduced by their respective report tasks; this file freezes the shared
// idempotency wrapper they must use rather than implementing a report-local
// replay store.
package reporthttp

import (
	"context"
	"encoding/json"
	"net/http"

	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

const maxReportMutationBodyBytes = 8 << 20

type IdentityResolver func(context.Context) (platformidempotency.Identity, error)

func WithIdempotency(
	repository platformidempotency.Repository,
	resolve IdentityResolver,
	next http.Handler,
) http.Handler {
	return platformidempotency.Middleware(platformidempotency.MiddlewareOptions{
		Repository: repository, ResolveIdentity: platformidempotency.IdentityResolver(resolve),
		Requires: platformidempotency.RequiresGovernedWrite,
		WriteError: func(writer http.ResponseWriter, status int, code, message string) {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(status)
			_ = json.NewEncoder(writer).Encode(map[string]string{"code": code, "message": message})
		},
		MaxRequestBytes: maxReportMutationBodyBytes,
	}, next)
}
