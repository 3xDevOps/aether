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
	// Timeline reads persisted workspace history ().
	Timeline TimelineReader
	// Costs is token metering, rollups, and workspace budgets ().
	Costs CostService
	// Templates is task templates and their cron schedules ().
	Templates TemplateService
	// Patch renders a run checkout's diff against its fork point ().
	Patch RunPatcher
	// Disk reads the data directory's disk usage ().
	Disk DiskReader
	// Environments builds and rolls back workspace environment images ().
	Environments EnvironmentService
}
