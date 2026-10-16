package api

import (
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/liftedkilt/openchore/internal/discord"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/webhook"
)

// Auth bundles the authentication services the router needs.
type Auth struct {
	Sessions *SessionManager
	OIDC     *OIDCService
}

func NewRouter(s *store.Store, dispatcher *webhook.Dispatcher, auth Auth) (*chi.Mux, *ChoreHandler, *ReportsHandler, *CoachHandler) {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	discordNotifier := discord.NewNotifier(s)

	users := NewUserHandler(s, dispatcher)
	aiSvc := NewAIServices(s)
	chores := NewChoreHandler(s, dispatcher, discordNotifier, aiSvc)
	admin := NewAdminHandler(s, dispatcher)
	points := NewPointsHandler(s)
	rewards := NewRewardHandler(s, dispatcher)
	streaks := NewStreakHandler(s)
	webhooks := NewWebhookHandler(s)
	triggers := NewTriggerHandler(s)
	tokens := NewTokenHandler(s)
	pets := NewPetHandler(s)
	boosts := NewBoostHandler(s)
	setup := NewSetupHandler(s, auth.Sessions)
	reports := NewReportsHandler(s, dispatcher, discordNotifier, aiSvc)
	coach := NewCoachHandler(s, aiSvc, chores)
	reviews := NewReviewHandler(s)
	// L4 T+0 hook: every wrong question parsed out of a paper gets its
	// review card the moment it lands (see recordErrorItem).
	chores.SetSpacedRepetition(reviews)
	courses := NewCourseHandler(s)
	learner := NewLearnerHandler(s, reviews, aiSvc)
	authH := NewAuthHandler(s, auth.Sessions, dispatcher, auth.OIDC)
	oidcH := auth.OIDC

	// Serve uploaded photos
	_ = os.MkdirAll("data/uploads", 0750)
	r.Handle("/uploads/*", http.StripPrefix("/uploads/", http.FileServer(http.Dir("data/uploads"))))

	// Serve read-aloud chore audio
	r.Handle("/tts/*", ttsFileServer())

	r.Route("/api", func(r chi.Router) {
		r.Use(CheckOrigin(auth.OIDC.publicURL))

		// Public: list users (for profile selection screen)
		r.Get("/users", users.List)
		r.Get("/users/{id}", users.Get)

		// Public, read-only: per-user chores, points and streak. The wall
		// display (/ambient) shows these without anyone signing in.
		r.Get("/users/{id}/chores", users.GetChores)
		r.Get("/users/{id}/points", points.GetUserPoints)
		r.Get("/users/{id}/streak", streaks.GetUserStreak)

		// Sign-in: tap a profile (PIN when set) or continue with an OIDC provider
		r.Post("/auth/login", authH.Login)
		r.Post("/auth/logout", authH.Logout)
		r.Get("/auth/providers", oidcH.Providers)
		r.Get("/auth/options", oidcH.SignInOptions)
		r.Get("/auth/oidc/{provider}/start", oidcH.Start)
		r.Get("/auth/oidc/{provider}/callback", oidcH.Callback)

		// Public: chore trigger webhook (UUID is the auth)
		r.Post("/hooks/trigger/{uuid}", triggers.FireTrigger)

		// Public: grandparent boost links (P4-16) — the token in the
		// path is the elder's only credential, so no session needed.
		r.Get("/boost/{token}", boosts.GetBoostPage)
		r.Post("/boost/{token}", boosts.PostBoost)

		// Initial setup (only works when no users exist)
		r.Post("/setup", setup.Setup)

		// Authenticated routes (session cookie, session bearer, or API token)
		r.Group(func(r chi.Router) {
			r.Use(RequireUserOrToken(s, auth.Sessions))

			r.Get("/auth/me", authH.Me)
			r.Post("/auth/logout-everywhere", authH.LogoutEverywhere)
			r.Post("/auth/upload-link", authH.UploadLink)

			// The kid's pet (P4-13): the "emotional balance" layer,
			// gated by the pet_enabled setting (default off).
			r.Get("/pet", pets.GetPet)
			r.Post("/pet/hatch", pets.HatchPet)
			r.Post("/pet/feed", pets.FeedPet)
			r.Get("/users/{id}/identities", oidcH.ListIdentities)
			r.Delete("/users/{id}/identities/{identityID}", oidcH.UnlinkIdentity)

			r.Get("/users/{id}/redemptions", rewards.ListRedemptions)

			// Daily coach note: kids read their own (parent side hidden),
			// grown-ups read any kid's full note.
			r.Get("/users/{id}/coach-note", coach.GetNote)

			// Templates page conflict preview: how many open-ended daily
			// schedules a stop-daily import would pause (admin-only UI, but
			// the count itself harms nobody — any signed-in member can read
			// their own household's schedule counts).
			r.Get("/users/{id}/running-daily", chores.RunningDailySchedules)

			// Any user can update their own profile preferences
			r.Put("/users/{id}/theme", users.UpdateTheme)
			r.Put("/users/{id}/avatar", users.UpdateAvatar)
			r.Put("/users/{id}/line-color", users.UpdateLineColor)
			r.Put("/users/{id}/color", users.UpdateColor)
			r.Put("/users/{id}/pin", users.SetPin)
			r.Delete("/users/{id}/pin", users.ClearPin)

			// Any user can complete/uncomplete chores
			r.Post("/schedules/{scheduleID}/complete", chores.Complete)
			r.Delete("/schedules/{scheduleID}/complete", chores.Uncomplete)
			r.Post("/upload", chores.UploadPhoto)
			r.Put("/completions/{id}/photo", chores.AttachPhoto)

			// L0 multi-file proof: a completion can carry several photos
			// (up to 9), one video or one PDF. The single-photo channel
			// above stays; photo_url keeps the first image for list views.
			r.Post("/completions/{id}/attachments", chores.UploadAttachments)
			r.Get("/completions/{id}/attachments", chores.ListAttachments)
			r.Get("/attachment-files/{id}", chores.GetAttachmentFile)

			// L1 wrong-question book: the kid sees their own error items
			// and reports retry outcomes; parents can list any kid's via
			// ?user_id= (the admin report lives under /admin).
			r.Get("/error-items", chores.ListErrorItems)
			r.Get("/error-items/{id}", chores.GetErrorItem)
			r.Post("/error-items/{id}/review-result", chores.SubmitReviewResult)

			// L4 spaced repetition: the kid's live due queue and the
			// two-button grading (Again/Good — Anki's FAQ names the
			// Hard-instead-of-Again habit as FSRS's one uncorrectable
			// failure mode, so those buttons don't exist).
			r.Get("/review/today", reviews.GetReviewToday)
			r.Post("/review/{id}/grade", reviews.GradeReview)

			// L3 learner profile, kid side: the persistent AI feedback card
			// (deterministic — free, fast, offline-safe) and the error-book
			// scaffolding ladder (Khanmigo: hint → specific hint →
			// decomposition → worked answer, server-side staged).
			r.Get("/kid/ai-feedback", learner.HandleKidAIFeedback)
			r.Post("/kid/scaffold", learner.HandleKidScaffold)

			// L2 course system, kid side: the published course list with
			// progress, the course tree with per-unit state (locked/
			// unlocked/started/completed + lock_explanation), progress
			// reporting (the unlock engine's write path), and the
			// mastery (outcomes) view.
			r.Get("/courses", courses.ListCoursesForKid)
			r.Get("/courses/{id}", courses.GetCourseForKid)
			r.Post("/resources/{id}/progress", courses.ReportProgress)
			r.Get("/courses/{id}/outcomes", courses.ListCourseOutcomes)

			// Any user can view and redeem rewards
			r.Get("/rewards", rewards.List)
			r.Post("/rewards/{id}/redeem", rewards.Redeem)

			// Reward commitments — kids commit points toward a chosen reward
			// and watch progress instead of being tempted to drain spendable.
			r.Get("/users/{id}/commitments", rewards.ListCommitments)
			r.Post("/rewards/{id}/commit", rewards.Commit)
			r.Post("/commitments/{id}/contribute", rewards.Contribute)
			r.Put("/commitments/{id}/auto-contribute", rewards.SetAutoContribute)
			r.Delete("/commitments/{id}", rewards.BreakCommitment)
			r.Get("/pools/{id}", rewards.GetSharedPool)

			// Admin-only routes
			r.Group(func(r chi.Router) {
				r.Use(RequireAdmin)

				r.Post("/users", users.Create)
				r.Put("/users/{id}", users.Update)
				r.Delete("/users/{id}", users.DeleteUser)
				r.Put("/users/{id}/pause", users.Pause)
				r.Put("/users/{id}/unpause", users.Unpause)

				r.Get("/chores", chores.List)
				r.Post("/chores", chores.Create)
				r.Get("/chores/{id}", chores.Get)
				r.Put("/chores/{id}", chores.Update)
				r.Delete("/chores/{id}", chores.Delete)

				r.Get("/chores/{id}/schedules", chores.ListSchedules)
				r.Post("/chores/{id}/schedules", chores.CreateSchedule)
				r.Delete("/chores/{id}/schedules/{scheduleID}", chores.DeleteSchedule)
				r.Post("/schedules/{scheduleID}/excuse", chores.Excuse)

				// Chore triggers
				r.Get("/chores/{id}/triggers", triggers.ListForChore)
				r.Post("/chores/{id}/triggers", triggers.Create)
				r.Put("/triggers/{id}", triggers.Update)
				r.Delete("/triggers/{id}", triggers.Delete)

				// Settings
				r.Get("/admin/settings/{key}", admin.GetSetting)
				r.Put("/admin/settings/{key}", admin.SetSetting)

				// Approvals
				r.Get("/completions/pending", chores.ListPending)
				r.Post("/completions/{id}/approve", chores.Approve)
				r.Post("/completions/{id}/reject", chores.Reject)

				// Points management
				r.Get("/points/balances", points.GetAllBalances)
				r.Post("/points/adjust", points.Adjust)
				r.Get("/admin/users/{id}/decay", points.GetDecayConfig)
				r.Put("/admin/users/{id}/decay", points.SetDecayConfig)

				// Rewards management
				r.Get("/rewards/all", rewards.ListAll)
				r.Post("/rewards", rewards.Create)
				r.Put("/rewards/{id}", rewards.Update)
				r.Put("/rewards/{id}/assignments", rewards.SetAssignments)
				r.Delete("/rewards/{id}", rewards.Delete)
				r.Delete("/redemptions/{redemptionID}", rewards.UndoRedemption)

				// Streak rewards management
				r.Get("/admin/streak-rewards", streaks.ListRewards)
				r.Post("/admin/streak-rewards", streaks.CreateReward)
				r.Delete("/admin/streak-rewards/{id}", streaks.DeleteReward)

				// Grandparent boost link management (P4-16): mint,
				// list and revoke the elder's read-only share links.
				r.Get("/admin/users/{userID}/boost-links", boosts.ListLinks)
				r.Post("/admin/users/{userID}/boost-links", boosts.CreateLink)
				r.Delete("/admin/users/{userID}/boost-links/{linkID}", boosts.RevokeLink)

				// Any kid's pet for the parent status card (P4-13,
				// admin view of the pet's mood — same payload as /pet).
				r.Get("/admin/users/{userID}/pet", pets.GetPetForAdmin)

				// Config export
				r.Get("/admin/export-config", admin.ExportConfig)

				// Reports
				r.Get("/admin/reports", reports.GetReports)

				// Webhooks management
				r.Get("/admin/webhooks", webhooks.List)
				r.Post("/admin/webhooks", webhooks.Create)
				r.Put("/admin/webhooks/{id}", webhooks.Update)
				r.Delete("/admin/webhooks/{id}", webhooks.Delete)
				r.Get("/admin/webhooks/{id}/deliveries", webhooks.ListDeliveries)

				// API token management
				r.Get("/admin/tokens", tokens.List)
				r.Post("/admin/tokens", tokens.Create)
				r.Delete("/admin/tokens/{id}", tokens.Revoke)

				// Sign-in settings
				r.Get("/admin/auth/config", oidcH.AdminConfig)
				r.Put("/admin/auth/sessions", oidcH.UpdateSessionTTLs)
				r.Put("/admin/auth/options", oidcH.UpdateSignInOptions)
				r.Post("/admin/auth/providers", oidcH.CreateProvider)
				r.Put("/admin/auth/providers/{id}", oidcH.UpdateProvider)
				r.Delete("/admin/auth/providers/{id}", oidcH.DeleteProvider)
				r.Post("/admin/auth/providers/{id}/test", oidcH.TestProvider)

				// Optional AI and read-aloud audio
				r.Get("/admin/ai/status", chores.AIStatus)
				r.Get("/admin/ai/config", aiSvc.GetConfig)
				r.Put("/admin/ai/config", aiSvc.UpdateConfig)
				r.Post("/admin/ai/test", chores.TestAIReview)
				r.Post("/admin/ai/generate-description", chores.GenerateDescription)
				r.Get("/admin/reports/ai-summary", reports.GetAISummary)
				r.Post("/admin/tts/regenerate", chores.RegenerateAllTTS)
				r.Post("/chores/{id}/tts/regenerate", chores.RegenerateChoreTTS)

				// Built-in chore template packs (P2-9): browse, AI-tune,
				// then import in one transaction (schedule swap included).
				r.Get("/admin/templates", chores.ListTemplates)
				r.Post("/admin/templates/{id}/tune", chores.TuneTemplate)
				r.Post("/admin/templates/import", chores.ImportTemplate)

				// L0 task templates: a parent's own saved chores, reusable
				// in one tap. Distinct namespace from the built-in packs.
				r.Get("/admin/task-templates", chores.ListTaskTemplates)
				r.Post("/admin/task-templates", chores.CreateTaskTemplate)
				r.Put("/admin/task-templates/{id}", chores.UpdateTaskTemplate)
				r.Delete("/admin/task-templates/{id}", chores.ArchiveTaskTemplate)
				r.Post("/admin/task-templates/{id}/instantiate", chores.InstantiateTaskTemplate)
				r.Post("/admin/task-templates/{id}/sync", chores.SyncTaskTemplate)

				// L0: a parent removing one proof file from a pending
				// completion (soft delete — reversible).
				r.Delete("/admin/attachments/{id}", chores.DeleteAttachment)

				// Parent Copilot (P3-10/11): draft a task plan from a
				// sentence or a photographed school notice, review card by
				// card, then apply through the same paths as a manual create.
				r.Post("/admin/copilot/draft", chores.DraftWithCopilot)
				r.Post("/admin/copilot/apply", chores.ApplyCopilotDraft)

				// L3 learner profile, parent side: the Ranedeer-style student
				// config (depth/learning style/communication/tone/reasoning +
				// interest lists) and the aggregated profile page payload
				// (hero stats / KC matrix / event trend) from one fetch.
				r.Get("/admin/student-config", learner.HandleGetStudentConfig)
				r.Put("/admin/student-config", learner.HandlePutStudentConfig)
				r.Get("/admin/learner-profile", learner.HandleGetLearnerProfile)

				// L3 planner: the copilot's profile-aware mode — injects the
				// learner profile, forces cited source_refs (DeepTutor's
				// auditability), degrades to notes-only on schema failure.
				r.Post("/admin/copilot/plan", chores.DraftLearnerPlanHTTP)

				// L1 error items / wrong-question loop: whole-paper async
				// parse (poll the job), the wrong-question book the kid
				// reviews, and the correction chore hook.
				r.Post("/admin/analyses", chores.StartPaperAnalysis)
				r.Get("/admin/analyses/{id}", chores.GetPaperAnalysis)
				// The kid's finished analyses (newest first): the parent's
				// dashboard quick-report reads the freshest one.
				r.Get("/admin/users/{userID}/analyses", chores.ListKidAnalyses)
				r.Get("/admin/users/{userID}/error-book-report", chores.GetErrorBookReport)
				r.Post("/admin/error-items/{id}/correction-task", chores.CreateCorrectionTask)
				r.Put("/admin/error-items/{id}", chores.UpdateErrorItem)
				r.Delete("/admin/error-items/{id}", chores.DeleteErrorItem)

				// Learning-measure engine (034): the analysis→measures
				// distill pass, the parent's CRUD over them, and the AI
				// strengthen pass rewriting one against fresh error data.
				r.Get("/admin/measures", chores.ListLearningMeasures)
				r.Post("/admin/measures", chores.CreateLearningMeasure)
				r.Put("/admin/measures/{id}", chores.UpdateLearningMeasure)
				r.Delete("/admin/measures/{id}", chores.DeleteLearningMeasure)
				r.Post("/admin/measures/{id}/status", chores.SetLearningMeasureStatus)
				r.Post("/admin/measures/{id}/strengthen", chores.StrengthenMeasure)
				r.Post("/admin/analyses/{id}/measures", chores.DistillMeasures)

				// L4 spaced repetition, parent side: the mastery dashboard
				// (heatmap / 30-day load forecast / streak), the FSRS
				// scheduler dial (?user_id= per kid), and per-card suspend.
				r.Get("/admin/review/stats", reviews.GetReviewStats)
				r.Get("/admin/fsrs-params", reviews.GetParams)
				r.Put("/admin/fsrs-params", reviews.UpdateParams)
				r.Post("/admin/review/{id}/suspend", reviews.SuspendReview)

				// L2 course system, parent side: CRUD over the course
				// tree, publish/archive, clone (§5.5 reuse path), View As
				// Learner preview, and the outcome (mastery) lines.
				r.Get("/admin/courses", courses.ListCoursesAdmin)
				r.Post("/admin/courses", courses.CreateCourseAdmin)
				r.Get("/admin/courses/preview", courses.PreviewCourseAdmin)
				r.Post("/admin/courses/preview", courses.PreviewCourseAdmin)
				r.Get("/admin/courses/{id}", courses.GetCourseAdmin)
				r.Put("/admin/courses/{id}", courses.UpdateCourseAdmin)
				r.Delete("/admin/courses/{id}", courses.DeleteCourseAdmin)
				r.Post("/admin/courses/{id}/units", courses.CreateUnitAdmin)
				r.Post("/admin/courses/{id}/publish", courses.PublishCourseAdmin)
				r.Post("/admin/courses/{id}/archive", courses.ArchiveCourseAdmin)
				r.Post("/admin/courses/{id}/clone", courses.CloneCourseAdmin)
				r.Get("/admin/courses/{id}/outcomes", courses.ListOutcomesAdmin)
				r.Post("/admin/courses/{id}/outcomes", courses.CreateOutcomeAdmin)
				r.Put("/admin/units/{id}", courses.UpdateUnitAdmin)
				r.Delete("/admin/units/{id}", courses.DeleteUnitAdmin)
				r.Post("/admin/units/{id}/resources", courses.CreateResourceAdmin)
				r.Put("/admin/resources/{id}", courses.UpdateResourceAdmin)
				r.Delete("/admin/resources/{id}", courses.DeleteResourceAdmin)
				r.Delete("/admin/outcomes/{id}", courses.DeleteOutcomeAdmin)
				r.Post("/admin/outcomes/{id}/assessments", courses.CreateAssessmentAdmin)

				// Integration discovery: chores with triggers
				r.Get("/chores/triggerable", triggers.ListTriggerable)
			})
		})
	})

	return r, chores, reports, coach
}
