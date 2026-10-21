package model

import "time"

// DateFormat is the standard YYYY-MM-DD date layout used throughout the application.
const DateFormat = "2006-01-02"

// Chore categories
const (
	CategoryRequired = "required"
	CategoryCore     = "core"
	CategoryBonus    = "bonus"
)

// Completion statuses
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
	StatusExcused  = "excused"
)

// Point transaction reasons
const (
	ReasonChoreComplete    = "chore_complete"
	ReasonChoreUncomplete  = "chore_uncomplete"
	ReasonAdminAdjust      = "admin_adjust"
	ReasonRewardRedeem     = "reward_redeem"
	ReasonExpiryPenalty    = "expiry_penalty"
	ReasonPointsDecay      = "points_decay"
	ReasonMissedChore      = "missed_chore"
	ReasonCommitToGoal     = "commit_to_goal"
	ReasonGoalBreak        = "goal_break"
	ReasonPetFeed          = "pet_feed"
	ReasonBonusDrop        = "bonus_drop"
	ReasonGrandparentBoost = "grandparent_boost"
	ReasonReviewComplete   = "review_complete"
)

// Commitment statuses
const (
	CommitmentActive    = "active"
	CommitmentRedeemed  = "redeemed"
	CommitmentCancelled = "cancelled"
)

// Photo source modes
const (
	PhotoSourceChild    = "child"
	PhotoSourceExternal = "external"
	PhotoSourceBoth     = "both"
)

// Where a chore came from. Template chores are imported from the built-in
// packs (admin templates page), so the UI can badge them; flyer chores come
// from the Copilot's photo extraction (school notices and the like); the
// copilot source covers its free-form one-liner drafts; planner chores are
// the L3 learner-plan cards the parent adopted (作业帮 loop: the plan's
// adopted tasks, so next week's plan can compare against how they went).
const (
	SourceManual   = "manual"
	SourceTemplate = "template"
	SourceFlyer    = "flyer"
	SourceCopilot  = "copilot"
	SourcePlanner  = "planner"
)

// Assignment types
const (
	AssignmentIndividual = "individual"
	AssignmentFamily     = "family"
	AssignmentFCFS       = "fcfs"
)

// Expiry penalty modes
const (
	ExpiryBlock    = "block"
	ExpiryNoPoints = "no_points"
	ExpiryPenalty  = "penalty"
)

// User is a household member. AuthProviders lists the IDs of the OIDC
// providers linked to the profile so the login screen can offer "Continue
// with ..." after the profile is tapped; subjects and emails are never
// exposed on the public listing.
type User struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	AvatarURL      string    `json:"avatar_url"`
	Role           string    `json:"role"`
	Age            *int      `json:"age,omitempty"`
	Theme          string    `json:"theme,omitempty"`
	LineColor      string    `json:"line_color,omitempty"`
	Color          string    `json:"color,omitempty"`
	Paused         bool      `json:"paused"`
	HasPin         bool      `json:"has_pin"`
	PinHash        string    `json:"-"`
	PinLength      int       `json:"pin_length,omitempty"` // digits in the PIN; 0 = no PIN or not known yet
	AuthProviders  []string  `json:"auth_providers"`
	SessionVersion int64     `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

// User roles. Admins manage the household *and* take part like everyone else
// (chores, points, rewards, streaks); the role only gates management actions.
const (
	RoleAdmin = "admin"
	RoleChild = "child"
)

// Skins, stored in users.theme. An empty theme means "not chosen"; the
// client resolves it by age.
const (
	ThemeSunroom = "sunroom"
	ThemeBlocks  = "blocks"
	ThemeTint    = "tint"
)

// ValidTheme reports whether t is one of the skins the API accepts.
func ValidTheme(t string) bool {
	return t == ThemeSunroom || t == ThemeBlocks || t == ThemeTint
}

// PersonColors are the keys stored in users.color, in assignment order.
// They are keys, not hex values: each theme defines its own shade.
var PersonColors = []string{"coral", "mint", "butter", "sky", "rose", "leaf", "lilac", "sand"}

// ValidPersonColor reports whether c is one of PersonColors.
func ValidPersonColor(c string) bool {
	for _, k := range PersonColors {
		if k == c {
			return true
		}
	}
	return false
}

// NextPersonColor picks a colour for a new person given the colours already
// in use: the first unused one in palette order, or once all are taken the
// least-used one (so assignment keeps cycling round-robin).
func NextPersonColor(used []string) string {
	counts := make(map[string]int, len(PersonColors))
	for _, c := range used {
		counts[c]++
	}
	best := PersonColors[0]
	for _, c := range PersonColors[1:] {
		if counts[c] < counts[best] {
			best = c
		}
	}
	return best
}

// UserIdentity is an external OIDC identity linked to a profile.
type UserIdentity struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	Provider    string     `json:"provider"`
	Subject     string     `json:"-"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"display_name,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// OIDCProvider is a single sign-on provider added from the admin UI.
// Scopes is space-separated; empty means "openid profile email".
type OIDCProvider struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"-"`
	Scopes       string    `json:"scopes"`
	Prompt       string    `json:"prompt"`
	CreatedAt    time.Time `json:"created_at"`
}

type Chore struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	// Subject names the school subject this chore belongs to (math/
	// chinese/english/physics/chemistry — the error_items vocabulary),
	// empty for household chores that are not school work. The media
	// library groups uploaded proof by it (036).
	Subject            string `json:"subject"`
	Icon               string `json:"icon,omitempty"`
	PointsValue        int    `json:"points_value"`
	MissedPenaltyValue int    `json:"missed_penalty_value"`
	EstimatedMinutes   *int   `json:"estimated_minutes,omitempty"`
	RequiresApproval   bool   `json:"requires_approval"`
	RequiresPhoto      bool   `json:"requires_photo"`
	PhotoSource        string `json:"photo_source"`
	ReferencePhotoURL  string `json:"reference_photo_url"`
	Source             string `json:"source"`
	// TemplateID, when set, points at the task template this chore was
	// instantiated from (L0); TemplateSyncedAt is the cursor for template
	// updates pushed to instances.
	TemplateID       *int64     `json:"template_id,omitempty"`
	TemplateSyncedAt *time.Time `json:"template_synced_at,omitempty"`
	ExternalID       string     `json:"external_id,omitempty"`
	TTSAudioURL      string     `json:"tts_audio_url,omitempty"`
	CreatedBy        int64      `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
}

type ChoreSchedule struct {
	ID             int64   `json:"id"`
	ChoreID        int64   `json:"chore_id"`
	AssignedTo     int64   `json:"assigned_to"`
	AssignmentType string  `json:"assignment_type"`
	FcfsGroupID    *string `json:"fcfs_group_id,omitempty"`
	DayOfWeek      *int    `json:"day_of_week,omitempty"`
	// DayOfMonth (问题④): the Nth of every month, 1-31. A 31 only fires in
	// 31-day months; shorter months skip the occurrence.
	DayOfMonth         *int    `json:"day_of_month,omitempty"`
	SpecificDate       *string `json:"specific_date,omitempty"`
	AvailableAt        *string `json:"available_at,omitempty"`
	PointsMultiplier   float64 `json:"points_multiplier"`
	StartDate          *string `json:"start_date,omitempty"`
	EndDate            *string `json:"end_date,omitempty"`
	RecurrenceInterval *int    `json:"recurrence_interval,omitempty"`
	RecurrenceStart    *string `json:"recurrence_start,omitempty"`
	DueBy              *string `json:"due_by,omitempty"`
	ExpiryPenalty      string  `json:"expiry_penalty"`
	ExpiryPenaltyValue int     `json:"expiry_penalty_value"`
	CreatedAt          string  `json:"created_at"`
}

type ChoreCompletion struct {
	ID              int64      `json:"id"`
	ChoreScheduleID int64      `json:"chore_schedule_id"`
	CompletedBy     int64      `json:"completed_by"`
	Status          string     `json:"status"` // approved, pending, rejected, excused
	PhotoURL        string     `json:"photo_url,omitempty"`
	ApprovedBy      *int64     `json:"approved_by,omitempty"`
	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	CompletedAt     time.Time  `json:"completed_at"`
	CompletionDate  string     `json:"completion_date"`
	// AIFeedback is the AI photo reviewer's note for the approving parent
	// (or, on an excused completion, the excuse reason).
	AIFeedback   string  `json:"ai_feedback,omitempty"`
	AIConfidence float64 `json:"ai_confidence,omitempty"`
	AIComplete   *bool   `json:"ai_complete,omitempty"`
	// UncompletedAt, when non-nil, marks a soft-deleted completion. The row
	// is preserved (photo + AI metadata + approval) so a kid can un-check and
	// re-check a chore without losing the approved state. Reader queries
	// exposing "is this done?" must treat non-nil UncompletedAt as not done.
	UncompletedAt *time.Time `json:"uncompleted_at,omitempty"`
}

// --- Points & Rewards ---

type PointTransaction struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	Amount         int       `json:"amount"`
	Reason         string    `json:"reason"`
	ReferenceID    *int64    `json:"reference_id,omitempty"`
	Note           string    `json:"note,omitempty"`
	IdempotencyKey *string   `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Reward struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Description   string             `json:"description"`
	Icon          string             `json:"icon,omitempty"`
	Cost          int                `json:"cost"`
	EffectiveCost int                `json:"effective_cost"` // per-user cost (may differ from base cost)
	Stock         *int               `json:"stock,omitempty"`
	Active        bool               `json:"active"`
	Shareable     bool               `json:"shareable"`
	CreatedBy     int64              `json:"created_by"`
	CreatedAt     time.Time          `json:"created_at"`
	Assignments   []RewardAssignment `json:"assignments,omitempty"`
}

type RewardAssignment struct {
	ID         int64 `json:"id"`
	RewardID   int64 `json:"reward_id"`
	UserID     int64 `json:"user_id"`
	CustomCost *int  `json:"custom_cost,omitempty"`
}

type RewardRedemption struct {
	ID          int64     `json:"id"`
	RewardID    int64     `json:"reward_id"`
	UserID      int64     `json:"user_id"`
	PointsSpent int       `json:"points_spent"`
	CreatedAt   time.Time `json:"created_at"`
}

// RewardCommitment represents a kid earmarking points toward a chosen reward.
// AmountSaved is derived from point_transactions referencing this commitment
// and is populated by the store layer (not stored on the row itself).
// SharedPoolID, when non-nil, marks this row as a kid's share of a pooled
// family goal — Pool is populated for caller convenience.
type RewardCommitment struct {
	ID                    int64                 `json:"id"`
	UserID                int64                 `json:"user_id"`
	RewardID              int64                 `json:"reward_id"`
	RewardName            string                `json:"reward_name,omitempty"`
	RewardIcon            string                `json:"reward_icon,omitempty"`
	TargetCost            int                   `json:"target_cost"`
	AmountSaved           int                   `json:"amount_saved"`
	AutoContributePercent int                   `json:"auto_contribute_percent"`
	Status                string                `json:"status"`
	SharedPoolID          *int64                `json:"shared_pool_id,omitempty"`
	Pool                  *SharedCommitmentPool `json:"pool,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	RedeemedAt            *time.Time            `json:"redeemed_at,omitempty"`
	CancelledAt           *time.Time            `json:"cancelled_at,omitempty"`
}

// SharedCommitmentPool is the pooled save target multiple kids are
// contributing to. AmountSaved is the sum across active contributors.
type SharedCommitmentPool struct {
	ID           int64             `json:"id"`
	RewardID     int64             `json:"reward_id"`
	RewardName   string            `json:"reward_name,omitempty"`
	RewardIcon   string            `json:"reward_icon,omitempty"`
	TargetCost   int               `json:"target_cost"`
	AmountSaved  int               `json:"amount_saved"`
	Status       string            `json:"status"`
	Contributors []PoolContributor `json:"contributors,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	RedeemedAt   *time.Time        `json:"redeemed_at,omitempty"`
}

// PoolContributor is one kid's slice of a shared pool — populated for the UI
// leaderboard ("You: 75, Alice: 120, Bob: 50").
type PoolContributor struct {
	UserID      int64  `json:"user_id"`
	UserName    string `json:"user_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	AmountSaved int    `json:"amount_saved"`
}

// --- Streaks ---

type UserStreak struct {
	UserID            int64   `json:"user_id"`
	CurrentStreak     int     `json:"current_streak"`
	LongestStreak     int     `json:"longest_streak"`
	StreakStartDate   *string `json:"streak_start_date,omitempty"`
	LastCompletedDate *string `json:"last_completed_date,omitempty"`
}

type StreakReward struct {
	ID          int64  `json:"id"`
	StreakDays  int    `json:"streak_days"`
	BonusPoints int    `json:"bonus_points"`
	Label       string `json:"label"`
}

// --- Decay ---

type UserDecayConfig struct {
	UserID             int64      `json:"user_id"`
	Enabled            bool       `json:"enabled"`
	DecayRate          int        `json:"decay_rate"`
	DecayIntervalHours int        `json:"decay_interval_hours"`
	LastDecayAt        *time.Time `json:"last_decay_at,omitempty"`
}

// --- Chore Triggers ---

type ChoreTrigger struct {
	ID                 int64   `json:"id"`
	UUID               string  `json:"uuid"`
	ChoreID            int64   `json:"chore_id"`
	DefaultAssignedTo  *int64  `json:"default_assigned_to,omitempty"`
	DefaultDueBy       *string `json:"default_due_by,omitempty"`
	DefaultAvailableAt *string `json:"default_available_at,omitempty"`
	Enabled            bool    `json:"enabled"`
	CooldownMinutes    int     `json:"cooldown_minutes"`
	AssignmentType     string  `json:"assignment_type"`
	LastTriggeredAt    *string `json:"last_triggered_at,omitempty"`
	CreatedAt          string  `json:"created_at"`
}

// --- Triggerable Chore (HA integration discovery) ---

type TriggerableChoreInfo struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Icon        string `json:"icon,omitempty"`
	PointsValue int    `json:"points_value"`
}

type TriggerInfo struct {
	ID                  int64   `json:"id"`
	UUID                string  `json:"uuid"`
	DefaultAssignedTo   *int64  `json:"default_assigned_to,omitempty"`
	DefaultAssignedName string  `json:"default_assigned_name,omitempty"`
	DefaultDueBy        *string `json:"default_due_by,omitempty"`
	DefaultAvailableAt  *string `json:"default_available_at,omitempty"`
	Enabled             bool    `json:"enabled"`
	CooldownMinutes     int     `json:"cooldown_minutes"`
}

type TriggerableChore struct {
	TriggerableChoreInfo
	Triggers []TriggerInfo `json:"triggers"`
}

// --- API Tokens ---

type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	Revoked    bool       `json:"revoked"`
	CreatedAt  time.Time  `json:"created_at"`
}

// --- Webhooks ---

type Webhook struct {
	ID        int64     `json:"id"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret,omitempty"`
	Events    string    `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type WebhookDelivery struct {
	ID           int64     `json:"id"`
	WebhookID    int64     `json:"webhook_id"`
	Event        string    `json:"event"`
	Payload      string    `json:"payload"`
	StatusCode   *int      `json:"status_code,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ScheduledChore is a denormalized view returned by the chores-for-user endpoint.
type ScheduledChore struct {
	ScheduleID         int64      `json:"schedule_id"`
	ChoreID            int64      `json:"chore_id"`
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	Category           string     `json:"category"`
	Subject            string     `json:"subject,omitempty"`
	Icon               string     `json:"icon,omitempty"`
	PointsValue        int        `json:"points_value"`
	MissedPenaltyValue int        `json:"missed_penalty_value"`
	EstimatedMinutes   *int       `json:"estimated_minutes,omitempty"`
	RequiresApproval   bool       `json:"requires_approval"`
	RequiresPhoto      bool       `json:"requires_photo"`
	PhotoSource        string     `json:"photo_source"`
	AssignmentType     string     `json:"assignment_type"`
	AvailableAt        *string    `json:"available_at,omitempty"`
	DueBy              *string    `json:"due_by,omitempty"`
	ExpiryPenalty      string     `json:"expiry_penalty"`
	ExpiryPenaltyValue int        `json:"expiry_penalty_value"`
	Available          bool       `json:"available"`
	Expired            bool       `json:"expired"`
	Completed          bool       `json:"completed"`
	CompletionID       *int64     `json:"completion_id,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	PhotoURL           *string    `json:"photo_url,omitempty"`
	CompletionStatus   *string    `json:"completion_status,omitempty"`
	AIFeedback         *string    `json:"ai_feedback,omitempty"`
	CompletedByName    string     `json:"completed_by_name,omitempty"`
	CompletedBySibling bool       `json:"completed_by_sibling,omitempty"`
	TTSAudioURL        string     `json:"tts_audio_url,omitempty"`
	ReferencePhotoURL  string     `json:"reference_photo_url,omitempty"`
	// ExternalID traces a chore back to its source object — the
	// correction task's "error_item:<id>|<来源>" (the kid's task row
	// renders「来自：卷面分析」from it) or the copilot's apply date.
	ExternalID string `json:"external_id,omitempty"`
	Date       string `json:"date"`
}

// AIReviewResult is an AI photo review: advice for a parent, never a verdict.
type AIReviewResult struct {
	Complete   bool    `json:"complete"`
	Confidence float64 `json:"confidence"`
	Feedback   string  `json:"feedback"`
}

// --- Daily coach notes (P2) ---

// DailyCoachNote is one day's two-sided coaching note: a short
// growth-minded line for the kid (with optional read-aloud audio) and a
// longer observation for the parent.
type DailyCoachNote struct {
	UserID        int64  `json:"user_id"`
	Date          string `json:"date"`
	ChildText     string `json:"child_text"`
	ChildAudioURL string `json:"child_audio_url,omitempty"`
	ParentText    string `json:"parent_text"`
}

// --- The kid's pet (P4-13) ---

// Pet is the kid's companion: the "emotional balance" layer. One per kid,
// fed with points (reason='pet_feed' in the ledger) and grown through
// cumulative feeding. The mood is never stored — it is computed from the
// kid's recent chore record, so it can't drift from reality. Pets never
// die; the worst mood is hungry.
type Pet struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	Name      string     `json:"name"`
	Stage     int        `json:"stage"`     // 1 young, 2 junior, 3 grown
	FedValue  int        `json:"fed_value"` // cumulative feeding → stage
	LastFedAt *time.Time `json:"last_fed_at,omitempty"`
	HatchedAt time.Time  `json:"hatched_at"`
}

// Pet moods, computed server-side from the kid's chore record.
const (
	PetMoodHappy  = "happy"  // today's required chores are done
	PetMoodCalm   = "calm"   // some required chores still open
	PetMoodHungry = "hungry" // required chores missed two days running
)

// Stage thresholds: cumulative fed_value unlocks the next stage.
const (
	PetStage2FedValue = 100
	PetStage3FedValue = 300
)

// FeedPetCost is what one feeding costs, in points.
const FeedPetCost = 10

// --- Grandparent boost links (P4-16) ---

// BoostLink is a share-out to a grown-up outside the household (a
// grandparent): a token-addressed, read-only page with a capped points
// "boost" button. The token is the only auth — elders never register —
// and the caps keep the boost a garnish, not a second income: a monthly
// allowance (default 60), at most 3 boosts per day per link.
type BoostLink struct {
	ID           int64      `json:"id"`
	UserID       int64      `json:"user_id"`         // the kid being cheered on
	Token        string     `json:"token,omitempty"` // shared once at creation; listings keep it private by zeroing it
	Label        string     `json:"label"`           // e.g. "Grandma"
	MonthlyCap   int        `json:"monthly_cap"`
	DailyLimit   int        `json:"daily_limit"`
	Granted      int        `json:"granted"`       // points granted in GrantedMonth
	GrantedMonth string     `json:"granted_month"` // "2006-01" the tally belongs to
	Revoked      bool       `json:"revoked"`
	ExpiresAt    time.Time  `json:"expires_at"`
	LastBoostAt  *time.Time `json:"last_boost_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// The boost link's default caps.
const (
	BoostDefaultMonthlyCap = 60
	BoostDefaultDailyLimit = 3
	BoostDefaultDays       = 90
)

// --- Completion attachments (L0) ---

// MimeClass buckets a proof file for rendering: images render inline and go
// to the AI reviewer, video, audio and PDF are parent-review only (Canvas
// mime_class). Audio is the direct-recording channel (a kid reading their
// poem aloud counts as proof the same way a photo does).
const (
	MimeClassImage = "image"
	MimeClassVideo = "video"
	MimeClassAudio = "audio"
	MimeClassPdf   = "pdf"
)

// MaxImageAttachments / MaxVideoAttachments / MaxAudioAttachments /
// MaxPdfAttachments cap one completion's proof set. Nine photos is a full
// multi-page workbook; a video, a recording or a PDF takes the place of the
// photos (one proof stream, not a pile of everything).
const (
	MaxImageAttachments = 9
	MaxVideoAttachments = 1
	MaxAudioAttachments = 1
	MaxPdfAttachments   = 1
)

// CompletionAttachment is one proof file on a completion. PhotoURL on the
// completion itself stays as the first-image convenience; this table is the
// source of truth for "what did the kid hand in".
type CompletionAttachment struct {
	ID            int64      `json:"id"`
	CompletionID  int64      `json:"completion_id"`
	FilePath      string     `json:"file_path"` // "/uploads/…" — the URL clients use
	DisplayName   string     `json:"display_name"`
	MimeType      string     `json:"mime_type"`
	SizeBytes     int64      `json:"size_bytes"`
	MimeClass     string     `json:"mime_class"`
	ThumbnailPath string     `json:"thumbnail_path,omitempty"`
	SHA256        string     `json:"sha256"`
	SortOrder     int        `json:"sort_order"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	// Per-attachment AI review (L0): the reviewer judges each image on its
	// own; the completion-level rollup is computed from these.
	AIFeedback   string  `json:"ai_feedback,omitempty"`
	AIConfidence float64 `json:"ai_confidence,omitempty"`
	AIComplete   *bool   `json:"ai_complete,omitempty"`
}

// AttachmentReviewResult pairs an attachment with the reviewer's verdict.
type AttachmentReviewResult struct {
	AttachmentID int64          `json:"attachment_id"`
	Review       AIReviewResult `json:"review"`
}

// --- Task templates (L0) ---

// TaskTemplate is a saved chore: a parent tunes a chore once (points, AI
// review prompt, reference photo, TTS) and instantiates it repeatedly.
// Archiving (IsArchived) replaces deleting — instantiated chores survive.
// ScheduleJSON snapshots the schedule shape (day_of_week / specific_date /
// recurrence) so instantiation can recreate it without guessing.
type TaskTemplate struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Category          string    `json:"category"`
	Points            int       `json:"points"`
	Description       string    `json:"description"`
	RequiresPhoto     bool      `json:"requires_photo"`
	PhotoSource       string    `json:"photo_source"`
	ReferencePhotoURL string    `json:"reference_photo_url,omitempty"`
	TTSAudioURL       string    `json:"tts_audio_url,omitempty"`
	AIPrompt          string    `json:"ai_prompt,omitempty"`
	ScheduleJSON      string    `json:"schedule_json"`
	CreatedBy         int64     `json:"created_by"`
	IsArchived        bool      `json:"is_archived"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// --- Error items / wrong-question loop (L1) ---

// Paper analysis statuses. A run is a small state machine: pending →
// running → done/failed; Stage tracks where inside running the two-stage
// parse is (segmenting vs judging) so the parent's progress bar can say
// more than a spinner.
const (
	AnalysisPending = "pending"
	AnalysisRunning = "running"
	AnalysisDone    = "done"
	AnalysisFailed  = "failed"
)

// Analysis stages (segment-then-judge, per the L1 spec's two-stage
// protocol).
const (
	StageQueued     = "queued"
	StageSegmenting = "segmenting"
	StageJudging    = "judging"
	StageDone       = "done"
)

// Mistake statuses: whether the student attempted the question at all —
// orthogonal to error_type (why it went wrong). 'unknown' is the degraded
// state when AI parsing failed; those items land in the manual queue
// (wrong-notebook's design intent: a failed parse never blocks the
// photo flow).
const (
	MistakeNotAttempted = "not_attempted"
	MistakeWrongAttempt = "wrong_attempt"
	MistakeUnknown      = "unknown"
)

// Error types (why it went wrong). calculation/concept are the workable
// starter set per the wrong-notebook evidence; reading/method extend it.
const (
	ErrorTypeCalculation = "calculation"
	ErrorTypeConcept     = "concept"
	ErrorTypeReading     = "reading"
	ErrorTypeMethod      = "method"
)

// Mastery levels (wrong-notebook masteryLevel, verbatim): does the kid
// know how to do this question now?
const (
	MasteryNew       = 0
	MasteryReviewing = 1
	MasteryMastered  = 2
)

// PaperAnalysis is one async whole-paper parse (POST /api/admin/analyses).
// The parent polls it; the report (Summary) holds the diagnosis JSON:
// error-type distribution, weak knowledge tags, and the per-question
// verdicts the report page renders.
type PaperAnalysis struct {
	ID             int64     `json:"id"`
	CompletionID   int64     `json:"completion_id"`
	UserID         int64     `json:"user_id"`
	CreatedBy      int64     `json:"created_by"`
	Subject        string    `json:"subject"`
	Instructions   string    `json:"instructions,omitempty"`
	Status         string    `json:"status"`
	Stage          string    `json:"stage"`
	Progress       int       `json:"progress"`
	TotalQuestions int       `json:"total_questions"`
	WrongCount     int       `json:"wrong_count"`
	Error          string    `json:"error,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ErrorItem is one wrong question (wrong-notebook ErrorItem, field for
// field, plus the OpenChore traceability columns). MasteryLevel is the
// three-state machine; NextReviewAt is a denormalized ordering column —
// L4's FSRS scheduler owns the real schedule.
type ErrorItem struct {
	ID               int64          `json:"id"`
	UserID           int64          `json:"user_id"`
	CompletionID     *int64         `json:"completion_id,omitempty"`
	AnalysisID       *int64         `json:"analysis_id,omitempty"`
	Subject          string         `json:"subject"`
	OriginalImageURL string         `json:"original_image_url"`
	OCRText          string         `json:"ocr_text,omitempty"`
	QuestionText     string         `json:"question_text,omitempty"`
	AnswerText       string         `json:"answer_text,omitempty"`
	WrongAnswerText  string         `json:"wrong_answer_text,omitempty"`
	Analysis         string         `json:"analysis,omitempty"`
	MistakeAnalysis  string         `json:"mistake_analysis,omitempty"`
	MistakeStatus    string         `json:"mistake_status"`
	ErrorType        string         `json:"error_type,omitempty"`
	Difficulty       string         `json:"difficulty,omitempty"`
	AIConfidence     float64        `json:"ai_confidence,omitempty"`
	PaperLevel       string         `json:"paper_level,omitempty"`
	GradeSemester    string         `json:"grade_semester,omitempty"`
	Source           string         `json:"source,omitempty"`
	MasteryLevel     int            `json:"mastery_level"`
	NextReviewAt     *time.Time     `json:"next_review_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Tags             []KnowledgeTag `json:"tags,omitempty"`
}

// LearningMeasure is one actionable learning measure (迁移 034): the
// analysis results distilled into something executable the parent can
// pause, finish or have the AI strengthen. Origin records where it came
// from — the paper analysis pipeline, the parent by hand, or an AI
// strengthen pass rewriting an existing measure.
type LearningMeasure struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	Subject    string    `json:"subject"`
	KCTagID    *int64    `json:"kc_tag_id,omitempty"`
	KCName     string    `json:"kc_name,omitempty"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	ErrorTypes []string  `json:"error_types"`
	Origin     string    `json:"origin"`
	OriginID   *int64    `json:"origin_id,omitempty"`
	Status     string    `json:"status"`
	Priority   int       `json:"priority"`
	CreatedBy  int64     `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Learning-measure origins (where a measure came from).
const (
	MeasureOriginAnalysis   = "analysis"
	MeasureOriginManual     = "manual"
	MeasureOriginStrengthen = "ai_strengthen"
)

// Learning-measure statuses.
const (
	MeasureStatusActive = "active"
	MeasureStatusPaused = "paused"
	MeasureStatusDone   = "done"
)

// KnowledgeTag is one knowledge point. Adjacency list, never JSON: the
// upstream project's own [DEPRECATED] marker on its JSON column is the
// lesson learned the hard way.
type KnowledgeTag struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	ParentID  *int64 `json:"parent_id,omitempty"`
	Code      string `json:"code,omitempty"`
	SortOrder int    `json:"sort_order"`
	IsSystem  bool   `json:"is_system"`
}

// Review-item source types (L4): what a spaced-repetition card was
// created from. L1 contributes error_item now; kc and course_outcome
// are the L2/L3 hooks (the scheduler never looks inside — it only
// carries source_type + source_id back to the caller).
const (
	ReviewSourceErrorItem = "error_item"
	ReviewSourceKC        = "kc"
	ReviewSourceOutcome   = "course_outcome"
)

// Review card states, the go-fsrs State names as stored in review_items.
// TEXT in SQL (readable diffs) matched against the library's own
// State.String(); the store layer converts both ways.
const (
	ReviewStateNew        = "New"
	ReviewStateLearning   = "Learning"
	ReviewStateReview     = "Review"
	ReviewStateRelearning = "Relearning"
)

// Review ratings, the go-fsrs Rating names as stored in review_logs.
// The kid's UI only exposes Again and Good (Anki's own FAQ names the
// Hard-instead-of-Again habit as FSRS's one uncorrectable failure mode,
// so the buttons that would let a child form it simply don't exist).
const (
	ReviewRatingAgain = "Again"
	ReviewRatingHard  = "Hard"
	ReviewRatingGood  = "Good"
	ReviewRatingEasy  = "Easy"
)

// ReviewItem is one spaced-repetition card (L4): the go-fsrs Card fields
// (due/stability/…/remaining_steps — owned by the scheduler, nothing
// else writes them) plus the front/back text the kid sees. One card per
// source object (UNIQUE(source_type, source_id)); suspended is the
// parent's pause switch.
type ReviewItem struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"user_id"`
	SourceType     string     `json:"source_type"`
	SourceID       int64      `json:"source_id"`
	FrontText      string     `json:"front_text"`
	BackText       string     `json:"back_text,omitempty"`
	Subject        string     `json:"subject,omitempty"`
	Due            time.Time  `json:"due"`
	Stability      float64    `json:"stability"`
	Difficulty     float64    `json:"difficulty"`
	ScheduledDays  int64      `json:"scheduled_days"`
	Reps           int64      `json:"reps"`
	Lapses         int64      `json:"lapses"`
	State          string     `json:"state"`
	LastReview     *time.Time `json:"last_review,omitempty"`
	RemainingSteps int        `json:"remaining_steps"`
	Suspended      bool       `json:"suspended"`
	CreatedAt      time.Time  `json:"created_at"`

	// The traced source object, joined in for the review screen (the
	// subject badge and the "来自：期中卷 第 17 题" provenance line).
	ErrorItem *ErrorItem `json:"error_item,omitempty"`
}

// ReviewLog is one rating event (the go-fsrs ReviewLog row): the stats
// views (heatmap, load forecast, retention curve) read these.
type ReviewLog struct {
	ID            int64     `json:"id"`
	ReviewItemID  int64     `json:"review_item_id"`
	UserID        int64     `json:"user_id"`
	Rating        string    `json:"rating"`
	State         string    `json:"state"`
	ElapsedDays   *int64    `json:"elapsed_days,omitempty"`
	ScheduledDays int64     `json:"scheduled_days"`
	ReviewedAt    time.Time `json:"reviewed_at"`
}

// FSRSParams is the per-kid scheduler config (fsrs_params row). A
// missing row means the defaults below; the parent's screen edits only
// request_retention (the one dial Anki's own docs call "the most
// important setting") and the daily new-card cap (a settings key, not
// here — it shapes the queue, not the scheduler).
type FSRSParams struct {
	UserID              int64     `json:"user_id"`
	RequestRetention    float64   `json:"request_retention"`
	MaximumInterval     int64     `json:"maximum_interval"`
	LearningStepsJSON   string    `json:"learning_steps_json"`
	RelearningStepsJSON string    `json:"relearning_steps_json"`
	EnableFuzz          bool      `json:"enable_fuzz"`
	WeightsJSON         string    `json:"weights,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// --- Course system (L2): Canvas Modules mapped onto SQLite ---

// Course statuses (edX draft→published: edits to a published course never
// mutate what the kid is looking at until the parent re-publishes).
const (
	CourseStatusDraft     = "draft"
	CourseStatusPublished = "published"
	CourseStatusArchived  = "archived"
)

// Unit requirement types (Canvas Module requirement_type): does finishing
// the unit need ALL resources complete or ANY one of them.
const (
	UnitRequirementAll = "all"
	UnitRequirementOne = "one"
)

// Resource types (Canvas ModuleItem type matrix, cut to the household
// set). 'task' is the chore bridge: the resource generates a real chore
// through the existing pipeline (§5.3 — courses generate tasks, never
// replace them).
const (
	ResourceTypePage     = "page"
	ResourceTypeFile     = "file"
	ResourceTypeAudio    = "audio"
	ResourceTypeVideo    = "video"
	ResourceTypePractice = "practice"
	ResourceTypeLink     = "link"
	ResourceTypeTask     = "task"
)

// Completion predicate types (Canvas completion_requirement, six enums
// cut to four for a household). NULL/"" = display-only, never gates.
const (
	CompletionMustView   = "must_view"   // viewed_seconds > 0
	CompletionMustSubmit = "must_submit" // submitted_ref non-empty
	CompletionMinScore   = "min_score"   // score >= min_score
	CompletionMinMinutes = "min_minutes" // viewed_seconds >= min_minutes*60
)

// Outcome calculation methods (Canvas Outcomes, six enums cut to four;
// n_mastery/weighted_average stay out of a household product).
const (
	OutcomeCalcLatest          = "latest"
	OutcomeCalcHighest         = "highest"
	OutcomeCalcAverage         = "average"
	OutcomeCalcDecayingAverage = "decaying_average"
)

// Assessment types (Canvas RubricAssessment, three values).
const (
	AssessmentTypeAI     = "ai"
	AssessmentTypeParent = "parent"
	AssessmentTypeSelf   = "self"
)

// Unit states as the kid's course tree reports them (locked/unlocked/
// started/completed — Canvas content_details, plus the lock_explanation
// the locked bubble shows).
const (
	UnitStateLocked    = "locked"
	UnitStateUnlocked  = "unlocked"
	UnitStateStarted   = "started"
	UnitStateCompleted = "completed"
)

// Course is the course shell. IsTemplate marks a reusable master; §5.5
// ruled against a separate course-template table (Canvas/edX both clone:
// edX Course Reruns, Canvas blueprint) so cloning a whole tree is the
// one reuse path.
type Course struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	SyllabusMD  string    `json:"syllabus_md"`
	Status      string    `json:"status"`
	IsTemplate  bool      `json:"is_template"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Course progress (kid side): percent complete and per-unit counts,
	// joined in by the store, never stored. Zero progress is a real
	// value the progress bar renders, so no omitempty here.
	ProgressPercent int `json:"progress_percent"`
	UnitsTotal      int `json:"units_total"`
	UnitsCompleted  int `json:"units_completed"`
}

// CourseUnit is the Canvas Module: position 1-based, prerequisites as a
// JSON int array (prerequisite_module_ids verbatim), and the optional
// unlock_at gate (NULL = immediately eligible once prerequisites clear).
type CourseUnit struct {
	ID                        int64      `json:"id"`
	CourseID                  int64      `json:"course_id"`
	Title                     string     `json:"title"`
	Position                  int        `json:"position"`
	PrerequisiteUnitIDsJSON   string     `json:"prerequisite_unit_ids_json"`
	RequirementType           string     `json:"requirement_type"`
	RequireSequentialProgress bool       `json:"require_sequential_progress"`
	UnlockAt                  *time.Time `json:"unlock_at,omitempty"`
	Published                 bool       `json:"published"`
}

// UnitResource is the Canvas ModuleItem: a typed resource plus its
// completion predicate. Task-type resources bridge into the chore
// pipeline; the predicate decides when the resource counts as done.
type UnitResource struct {
	ID             int64  `json:"id"`
	UnitID         int64  `json:"unit_id"`
	Title          string `json:"title"`
	Position       int    `json:"position"`
	ResourceType   string `json:"resource_type"`
	ContentRef     string `json:"content_ref"`
	CompletionType string `json:"completion_type,omitempty"`
	MinScore       *int   `json:"min_score,omitempty"`
	MinMinutes     *int   `json:"min_minutes,omitempty"`
}

// ResourceAttempt is the completion fact row for one (resource, kid):
// overwritten on retry (UNIQUE constraint), it is the only thing the
// unlock engine reads to decide "is this resource done".
type ResourceAttempt struct {
	ID            int64      `json:"id"`
	ResourceID    int64      `json:"resource_id"`
	UserID        int64      `json:"user_id"`
	ViewedSeconds int64      `json:"viewed_seconds"`
	SubmittedRef  string     `json:"submitted_ref,omitempty"`
	Score         *int       `json:"score,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// CourseOutcome is the Canvas Outcome: the mastery line per competency
// ("能自主读完一本章节书"), rolled up from assessments per
// calculation_method against mastery_points (the 100-scale pass line).
type CourseOutcome struct {
	ID                int64  `json:"id"`
	CourseID          int64  `json:"course_id"`
	Title             string `json:"title"`
	MasteryPoints     int    `json:"mastery_points"`
	CalculationMethod string `json:"calculation_method"`
	CalcInt           *int   `json:"calc_int,omitempty"`
	RatingsJSON       string `json:"ratings_json"`
}

// OutcomeAssessment is one score event (Canvas RubricAssessment): who
// graded (ai/parent/self), the score, and the optional artifact pointer.
type OutcomeAssessment struct {
	ID             int64     `json:"id"`
	OutcomeID      int64     `json:"outcome_id"`
	UserID         int64     `json:"user_id"`
	Score          int       `json:"score"`
	AssessmentType string    `json:"assessment_type"`
	ArtifactType   string    `json:"artifact_type,omitempty"`
	ArtifactID     *int64    `json:"artifact_id,omitempty"`
	Comments       string    `json:"comments,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// --- L3 · Learner profile & AI planning ---

// LearnerEvent is the xAPI Statement skeleton (actor/verb/object/result),
// append-only: every learning act (chore completion, error review, course
// practice, spaced review) lands here once and is never updated. The full
// LRS (Tin Can endpoints, voiding) is deliberately out — a single-household
// app doesn't need it.
type LearnerEvent struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"user_id"`
	Verb        string    `json:"verb"`
	ObjectType  string    `json:"object_type"`
	ObjectID    int64     `json:"object_id"`
	ResultJSON  string    `json:"result_json,omitempty"`
	ContextJSON string    `json:"context_json,omitempty"`
	OccurredAt  time.Time `json:"occurred_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// The verb set (xAPI verbs, trimmed to what OpenChore actually emits).
const (
	EventVerbCompleted = "completed" // a chore/course resource finished
	EventVerbSubmitted = "submitted" // work handed in (photo, paper)
	EventVerbReviewed  = "reviewed"  // an FSRS review graded
	EventVerbAnswered  = "answered"  // an error-item re-test answered
	EventVerbAttempted = "attempted" // a practice attempt with a score
)

// The object set (what the verb acted on).
const (
	EventObjectChore      = "chore"
	EventObjectErrorItem  = "error_item"
	EventObjectResource   = "resource"
	EventObjectReviewItem = "review_item"
	EventObjectCourse     = "course"
)

// KCMastery is the per-(kid, knowledge tag) rollup: Laplace-smoothed
// correct rate over the recent window, materialized so the profile page
// and the planner read one row instead of scanning events. LowConfidence
// marks thin samples (< 5) so the UI greys them out and the planner
// down-weights them.
type KCMastery struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	TagID         int64      `json:"tag_id"`
	CorrectCount  int        `json:"correct_count"`
	TotalCount    int        `json:"total_count"`
	Mastery       float64    `json:"mastery"`
	LowConfidence bool       `json:"low_confidence"`
	LastEventAt   *time.Time `json:"last_event_at,omitempty"`
}

// StudentConfig is the Mr.-Ranedeer configuration cut down to the five
// enums a parent can actually tune (depth, learning style, communication,
// tone, reasoning — Ranedeer's own documented values, K12 slice), plus
// free-form interest/strength/weakness lists that the profile shows and
// the planner injects.
type StudentConfig struct {
	UserID        int64     `json:"user_id"`
	Depth         string    `json:"depth"`
	LearningStyle string    `json:"learning_style"`
	Communication string    `json:"communication"`
	Tone          string    `json:"tone"`
	Reasoning     string    `json:"reasoning"`
	Interests     string    `json:"interests"`
	Strengths     string    `json:"strengths"`
	Weaknesses    string    `json:"weaknesses"`
	UpdatedAt     time.Time `json:"updated_at"`
}
