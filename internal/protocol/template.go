package protocol

// Wave 4 task-template and cron-schedule methods.
const (
	// MethodTemplateList lists a session's templates.
	MethodTemplateList = "template.list"
	// MethodTemplateSave creates or replaces one (session administration).
	MethodTemplateSave = "template.save"
	// MethodTemplateDelete removes one and its schedule (session
	// administration).
	MethodTemplateDelete = "template.delete"
	// MethodTemplateLaunch launches a template as the caller (Launch).
	MethodTemplateLaunch = "template.launch"
	// MethodScheduleList lists a session's cron schedules.
	MethodScheduleList = "schedule.list"
	// MethodScheduleSave creates or replaces a template's cron rule
	// (Launch: a schedule is a standing order for future runs).
	MethodScheduleSave = "schedule.save"
	// MethodScheduleDelete removes a template's cron rule (Launch).
	MethodScheduleDelete = "schedule.delete"
)

// Template is the wire form of a task template. Params are the task
// prompt's {{placeholder}} defaults; BudgetUSD is an advisory hint, not a
// cap.
type Template struct {
	ID        string            `json:"id"`
	SessionID string            `json:"session_id"`
	Name      string            `json:"name"`
	Task      string            `json:"task"`
	Harness   string            `json:"harness"`
	Mode      string            `json:"mode"`
	Params    map[string]string `json:"params,omitempty"`
	BudgetUSD float64           `json:"budget_usd,omitempty"`
	CreatedAt string            `json:"created_at"`
}

// Schedule is the wire form of a cron schedule. NextFire is empty when the
// server has not scheduled it yet.
type Schedule struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	Template   string `json:"template"`
	Cron       string `json:"cron"`
	MemberID   string `json:"member_id"`
	CreatedAt  string `json:"created_at"`
	LastFireAt string `json:"last_fire_at,omitempty"`
	NextFireAt string `json:"next_fire_at,omitempty"`
}

// TemplateListParams selects one session's templates.
type TemplateListParams struct {
	SessionID string `json:"session_id"`
}

// TemplateListResult is the session's templates by name.
type TemplateListResult struct {
	Templates []Template `json:"templates"`
}

// TemplateSaveParams creates or replaces a template. An empty Mode
// defaults to headless.
type TemplateSaveParams struct {
	SessionID string            `json:"session_id"`
	Name      string            `json:"name"`
	Task      string            `json:"task"`
	Harness   string            `json:"harness"`
	Mode      string            `json:"mode,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	BudgetUSD float64           `json:"budget_usd,omitempty"`
}

// TemplateSaveResult carries the stored template.
type TemplateSaveResult struct {
	Template Template `json:"template"`
}

// TemplateDeleteParams names the template to remove.
type TemplateDeleteParams struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// TemplateLaunchParams launches a template; Params override its defaults.
type TemplateLaunchParams struct {
	SessionID string            `json:"session_id"`
	Name      string            `json:"name"`
	Params    map[string]string `json:"params,omitempty"`
}

// TemplateLaunchResult is the launched run plus the age of the base
// branch it started from, as the server last saw it. BaseAge is empty when
// the server has never seen a commit on that branch - the honest answer
// when no member has pushed it.
type TemplateLaunchResult struct {
	Run        Run    `json:"run"`
	BaseBranch string `json:"base_branch"`
	BaseAge    string `json:"base_age,omitempty"`
}

// ScheduleListParams selects one session's schedules.
type ScheduleListParams struct {
	SessionID string `json:"session_id"`
}

// ScheduleListResult is the session's schedules.
type ScheduleListResult struct {
	Schedules []Schedule `json:"schedules"`
}

// ScheduleSaveParams sets a template's cron rule, in standard five-field
// cron syntax or a @descriptor. Rules are evaluated in UTC, like every
// other instant Aether reports.
type ScheduleSaveParams struct {
	SessionID string `json:"session_id"`
	Template  string `json:"template"`
	Cron      string `json:"cron"`
}

// ScheduleSaveResult carries the stored schedule.
type ScheduleSaveResult struct {
	Schedule Schedule `json:"schedule"`
}

// ScheduleDeleteParams names the template whose rule to remove.
type ScheduleDeleteParams struct {
	SessionID string `json:"session_id"`
	Template  string `json:"template"`
}
