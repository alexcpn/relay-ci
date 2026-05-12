package main

import (
	"log/slog"

	"github.com/ci-system/ci/pkg/store"
)

// auditor writes structured audit entries to the build store.
// All methods are no-ops when the store is nil.
type auditor struct {
	store  store.Store
	logger *slog.Logger
}

func newAuditor(st store.Store, logger *slog.Logger) *auditor {
	return &auditor{store: st, logger: logger}
}

func (a *auditor) log(actor, action, resource, detail string) {
	if a.store == nil {
		return
	}
	if err := a.store.AppendAudit(&store.AuditEntry{
		Actor:    actor,
		Action:   action,
		Resource: resource,
		Detail:   detail,
	}); err != nil {
		a.logger.Error("audit log write failed", "action", action, "err", err)
	}
}

// Convenience wrappers for the common actions.

func (a *auditor) BuildSubmit(actor, buildID, repo string) {
	a.log(actor, "build.submit", buildID, repo)
}

func (a *auditor) BuildCancel(actor, buildID string) {
	a.log(actor, "build.cancel", buildID, "")
}

func (a *auditor) BuildRetry(actor, buildID string) {
	a.log(actor, "build.retry", buildID, "")
}

func (a *auditor) SecretPut(actor, scope, name string) {
	a.log(actor, "secret.put", scope+"/"+name, "")
}

func (a *auditor) SecretDelete(actor, scope, name string) {
	a.log(actor, "secret.delete", scope+"/"+name, "")
}

func (a *auditor) PipelinePin(actor, projectID, digest string) {
	a.log(actor, "pipeline.pin", projectID, digest)
}

func (a *auditor) PipelineUnpin(actor, projectID string) {
	a.log(actor, "pipeline.unpin", projectID, "")
}
