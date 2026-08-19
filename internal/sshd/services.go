package sshd

// Services carries the team-feature service seams the control-channel
// handlers reach. Every field is optional: a handler whose service is nil
// answers CodeUnavailable, the same degradation Config.Profiles uses.
// Each interface is declared in the file of the feature that owns it, so
// features extend this seam set without contending for one file.
type Services struct {
	// Approvals is the shared approval inbox and presence roster ().
	Approvals ApprovalService
	// Overlaps is the cross-run file overlap index ().
	Overlaps OverlapIndex
	// Timeline reads persisted session history ().
	Timeline TimelineReader
	// Costs is token metering, rollups, and session budgets ().
	Costs CostService
	// Templates is task templates and their cron schedules ().
	Templates TemplateService
	// Dashboard mints the web gateway's bearer tokens ().
	Dashboard DashboardTokens
}
