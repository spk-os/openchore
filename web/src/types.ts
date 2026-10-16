// A person's skin (users.theme). An empty theme resolves by age on the client.
export type Theme = 'sunroom' | 'blocks' | 'tint';

// A person's colour key (users.color). Each theme defines its own shade.
export type PersonColor = 'coral' | 'mint' | 'butter' | 'sky' | 'rose' | 'leaf' | 'lilac' | 'sand';

export const PERSON_COLORS: readonly PersonColor[] = ['coral', 'mint', 'butter', 'sky', 'rose', 'leaf', 'lilac', 'sand'];

export interface SoundDef {
  notes: { freq: number; duration: number; delay: number }[];
  waveform: OscillatorType;
  gain: number;
}

// What a skin may change about feedback. Words, categories and greetings are
// shared by every skin (design i18n); only sounds and the buzz differ.
export interface ThemeConfig {
  sounds: {
    complete: SoundDef;
    allDone: SoundDef;
  };
  /** navigator.vibrate pattern on finishing a chore. */
  vibrate: number | number[];
}

export const THEME_CONFIG: Record<Theme, ThemeConfig> = {
  sunroom: {
    sounds: {
      complete: {
        notes: [
          { freq: 523, duration: 0.12, delay: 0 },
          { freq: 659, duration: 0.18, delay: 0.1 },
        ],
        waveform: 'sine',
        gain: 0.15,
      },
      allDone: {
        notes: [
          { freq: 523, duration: 0.12, delay: 0 },
          { freq: 659, duration: 0.12, delay: 0.1 },
          { freq: 784, duration: 0.12, delay: 0.2 },
          { freq: 1047, duration: 0.3, delay: 0.3 },
        ],
        waveform: 'sine',
        gain: 0.15,
      },
    },
    vibrate: 50,
  },
  blocks: {
    sounds: {
      complete: {
        notes: [
          { freq: 262, duration: 0.1, delay: 0 },
          { freq: 330, duration: 0.1, delay: 0.08 },
          { freq: 392, duration: 0.2, delay: 0.16 },
        ],
        waveform: 'triangle',
        gain: 0.12,
      },
      allDone: {
        notes: [
          { freq: 262, duration: 0.1, delay: 0 },
          { freq: 330, duration: 0.1, delay: 0.08 },
          { freq: 392, duration: 0.1, delay: 0.16 },
          { freq: 523, duration: 0.15, delay: 0.24 },
          { freq: 659, duration: 0.15, delay: 0.35 },
          { freq: 784, duration: 0.35, delay: 0.46 },
        ],
        waveform: 'triangle',
        gain: 0.12,
      },
    },
    vibrate: [30, 40, 30],
  },
  tint: {
    sounds: {
      complete: {
        notes: [
          { freq: 880, duration: 0.08, delay: 0 },
          { freq: 1100, duration: 0.06, delay: 0.06 },
          { freq: 1320, duration: 0.12, delay: 0.1 },
        ],
        waveform: 'sine',
        gain: 0.1,
      },
      allDone: {
        notes: [
          { freq: 440, duration: 0.08, delay: 0 },
          { freq: 554, duration: 0.08, delay: 0.06 },
          { freq: 659, duration: 0.08, delay: 0.12 },
          { freq: 880, duration: 0.1, delay: 0.18 },
          { freq: 1100, duration: 0.1, delay: 0.26 },
          { freq: 1320, duration: 0.25, delay: 0.34 },
        ],
        waveform: 'sine',
        gain: 0.1,
      },
    },
    vibrate: 40,
  },
};

export interface User {
  id: number;
  name: string;
  avatar_url: string;
  role: 'admin' | 'child';
  age?: number;
  theme: Theme;
  line_color?: string;
  color?: PersonColor;
  paused: boolean;
  has_pin: boolean;
  // Digits in the PIN, so the PIN pad submits on the last one. Absent when
  // there's no PIN or it was set before lengths were recorded.
  pin_length?: number;
  // IDs of OIDC providers linked to this profile ("Continue with ...").
  auth_providers: string[];
  created_at: string;
}

export interface SessionInfo {
  method: 'tap' | 'pin' | 'oidc' | 'upload';
  expires_at: string;
  // Personal-device (OIDC) sessions persist and skip the kiosk idle logout.
  persistent: boolean;
  provider?: string;
}

export interface AuthSession {
  user: User;
  session: SessionInfo;
}

export interface AuthProvider {
  id: string;
  name: string;
}

/** Shapes the sign-in screen; readable before anyone signs in. */
export interface SignInOptions {
  /** False: people with a linked account must use it instead of a PIN. */
  pin_sign_in: boolean;
  /** Where a signed-out visitor to / lands. /login and /ambient always work. */
  start_page: 'picker' | 'wall' | 'provider';
  start_provider?: string;
}

export interface LinkedIdentity {
  id: number;
  user_id: number;
  provider: string;
  email?: string;
  display_name?: string;
  created_at: string;
  last_login_at?: string;
}

export interface Chore {
  id: number;
  title: string;
  description: string;
  category: 'required' | 'core' | 'bonus';
  /** School subject (036): math/chinese/english/physics/chemistry, '' = none. */
  subject: string;
  icon?: string;
  points_value: number;
  missed_penalty_value: number;
  estimated_minutes?: number;
  requires_approval: boolean;
  requires_photo: boolean;
  photo_source?: 'child' | 'external' | 'both';
  /** Parent's "this is what done looks like" example photo. '' = none. */
  reference_photo_url?: string;
  tts_audio_url?: string;
}

export interface ChoreSchedule {
  id: number;
  chore_id: number;
  assigned_to: number;
  assignment_type: string;
  day_of_week?: number;
  specific_date?: string;
  available_at?: string;
  due_by?: string;
  expiry_penalty: 'block' | 'no_points' | 'penalty';
  expiry_penalty_value: number;
  points_multiplier: number;
  start_date?: string;
  end_date?: string;
  recurrence_interval?: number;
  recurrence_start?: string;
  created_at: string;
}

export const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as const;

// --- Points & Rewards ---

export interface PointTransaction {
  id: number;
  user_id: number;
  amount: number;
  reason: 'chore_complete' | 'chore_uncomplete' | 'reward_redeem' | 'streak_bonus' | 'admin_adjust' | 'expiry_penalty' | 'points_decay' | 'missed_chore' | 'commit_to_goal' | 'goal_break' | 'pet_feed' | 'bonus_drop' | 'grandparent_boost';
  reference_id?: number;
  note?: string;
  created_at: string;
}

export interface RewardCommitment {
  id: number;
  user_id: number;
  reward_id: number;
  reward_name?: string;
  reward_icon?: string;
  target_cost: number;
  amount_saved: number;
  auto_contribute_percent: number;
  status: 'active' | 'redeemed' | 'cancelled';
  shared_pool_id?: number;
  pool?: SharedCommitmentPool;
  created_at: string;
  redeemed_at?: string;
  cancelled_at?: string;
}

export interface PointsData {
  balance: number;
  committed: number;
  active_commitments: RewardCommitment[];
  transactions: PointTransaction[];
}

export interface PointBalance {
  user_id: number;
  balance: number;
}

export interface RewardAssignment {
  id: number;
  reward_id: number;
  user_id: number;
  custom_cost?: number;
}

export interface Reward {
  id: number;
  name: string;
  description: string;
  icon?: string;
  cost: number;
  effective_cost: number;
  stock?: number;
  active: boolean;
  shareable: boolean;
  created_by: number;
  created_at: string;
  assignments?: RewardAssignment[];
}

export interface PoolContributor {
  user_id: number;
  user_name: string;
  avatar_url?: string;
  amount_saved: number;
}

export interface SharedCommitmentPool {
  id: number;
  reward_id: number;
  reward_name?: string;
  reward_icon?: string;
  target_cost: number;
  amount_saved: number;
  status: 'active' | 'redeemed' | 'cancelled';
  contributors?: PoolContributor[];
  created_at: string;
  redeemed_at?: string;
}

export interface RewardRedemption {
  id: number;
  reward_id: number;
  user_id: number;
  points_spent: number;
  created_at: string;
}

// --- Streaks ---

export interface UserStreakData {
  current_streak: number;
  longest_streak: number;
  streak_start_date?: string;
  earned_rewards: StreakRewardItem[];
  next_reward?: StreakRewardItem & { days_remaining: number };
}

export interface StreakRewardItem {
  id: number;
  streak_days: number;
  bonus_points: number;
  label: string;
}

export interface ScheduledChore {
  schedule_id: number;
  chore_id: number;
  title: string;
  description: string;
  category: 'required' | 'core' | 'bonus';
  icon?: string;
  points_value: number;
  missed_penalty_value: number;
  estimated_minutes?: number;
  requires_approval: boolean;
  requires_photo: boolean;
  photo_source?: 'child' | 'external' | 'both';
  reference_photo_url?: string;
  assignment_type: string;
  available_at?: string;
  due_by?: string;
  expiry_penalty: 'block' | 'no_points' | 'penalty';
  expiry_penalty_value: number;
  available: boolean;
  expired: boolean;
  completed: boolean;
  completion_id?: number;
  completed_at?: string;
  photo_url?: string;
  date: string;
  completion_status?: 'approved' | 'pending' | 'rejected' | 'excused';
  // The chore's school subject (036 media library), empty for household
  // chores: math/chinese/english/physics/chemistry.
  subject?: string;
  ai_feedback?: string;
  completed_by_name?: string;
  completed_by_sibling?: boolean;
  tts_audio_url?: string;
  // The chore's external provenance (L1): "error_item:<id>|<来源>" on
  // correction tasks spawned by the paper analysis, rendered by the kid's
  // task row as 「来自：期中卷分析」.
  external_id?: string;
}

// Shape of rows returned by GET /api/completions/pending. Exposed as a typed
// surface (the admin UI cares about assigned_user_id to attribute pending
// approvals to the kid the chore belongs to, not just whoever clicked it).
export interface PendingCompletion {
  id: number;
  chore_id: number;
  chore_title: string;
  child_name: string;
  assigned_user_id: number;
  photo_url: string;
  completion_date: string;
  completed_at: string;
  // Who clicked "complete" (child_name is their name).
  completed_by?: number;
  category?: 'required' | 'core' | 'bonus';
  icon?: string;
  points_value?: number;
  // Whether the chore needs a photo, and who takes it. A "child" chore
  // pending without a photo was finished via "No photo? Finish anyway";
  // for "external"/"both" the photo can still be attached later.
  requires_photo?: boolean;
  photo_source?: 'child' | 'external' | 'both';
  // The chore's "this is what done looks like" example, when set: the
  // approval card shows it next to the submitted photo (before/after).
  reference_photo_url?: string;
  // The AI photo reviewer's advisory note, once a review has run.
  // ai_complete is its read on the photo (absent until reviewed).
  ai_feedback?: string;
  ai_confidence?: number;
  ai_complete?: boolean;
}

export interface UserDecayConfig {
  user_id: number;
  enabled: boolean;
  decay_rate: number;
  decay_interval_hours: number;
  last_decay_at?: string;
}

export interface RedemptionHistory {
  id: number;
  reward_name: string;
  reward_icon: string;
  points_spent: number;
  created_at: string;
}

export interface ChoreTrigger {
  id: number;
  uuid: string;
  chore_id: number;
  default_assigned_to?: number;
  default_due_by?: string;
  default_available_at?: string;
  enabled: boolean;
  cooldown_minutes: number;
  assignment_type: string;
  last_triggered_at?: string;
  created_at: string;
}

export interface Webhook {
  id: number;
  url: string;
  secret: string;
  events: string;
  active: boolean;
  created_at: string;
}

export interface APIToken {
  id: number;
  name: string;
  last_used_at?: string;
  revoked: boolean;
  created_at: string;
}

export interface WebhookDelivery {
  id: number;
  webhook_id: number;
  event: string;
  payload: string;
  status_code?: number;
  response_body?: string;
  error?: string;
  created_at: string;
}

// Which optional AI services the server has configured.
export interface AIStatus {
  ai: { configured: boolean; model?: string };
  tts: { configured: boolean; model?: string };
}

/** Where one OpenAI-compatible service lives. The key is write-only. */
export interface AIConnection {
  base_url: string;
  model: string;
  api_key_set: boolean;
  /** Set by AI_BASE_URL / TTS_BASE_URL: read-only in the UI. */
  from_env: boolean;
}

export interface AIConfig {
  ai: AIConnection;
  tts: AIConnection;
}

/** One section of an AI config update. Omit api_key to keep the saved one; '' clears it. */
export interface AIConnectionUpdate {
  base_url: string;
  model: string;
  api_key?: string;
}

/** A single sign-on provider as the admin Sign-in settings show it. */
export interface AdminAuthProvider {
  id: string;
  name: string;
  issuer: string;
  client_id: string;
  client_secret_set: boolean;
  scopes: string;
  prompt: string;
  /** 'config' = config.yaml or OIDC_* environment variables (read-only). */
  source: 'config' | 'settings';
  linked_accounts: number;
  redirect_uri: string;
}

export interface SessionLength {
  hours: number;
  default_hours: number;
  from_config: boolean;
}

export interface AdminAuthConfig {
  providers: AdminAuthProvider[];
  callback_base: string;
  public_url_from_config: boolean;
  kiosk_session: SessionLength;
  personal_session: SessionLength;
  sign_in: AdminSignInOptions;
}

export interface AdminSignInOptions {
  pin_sign_in: boolean;
  pin_sign_in_from_config: boolean;
  /** 'picker', 'wall' or 'provider:<id>'. */
  start_page: string;
  start_page_from_config: boolean;
}

/** Body of create/update provider. Omit client_secret on update to keep it. */
export interface AuthProviderInput {
  id?: string;
  name: string;
  issuer: string;
  client_id: string;
  client_secret?: string;
  scopes: string;
  prompt: string;
}

export interface AIReviewResult {
  complete: boolean;
  confidence: number;
  feedback: string;
  would_approve: boolean;
  elapsed_ms: number;
}

// --- Completion attachments (L0) ---

/** Canvas-style mime class: a completion's proof stream is images, a video, or a PDF. */
export type MimeClass = 'image' | 'video' | 'audio' | 'pdf';

/**
 * One proof file on a completion. Rows live in completion_attachments;
 * the completion's photo_url column only mirrors the first image. Each
 * image carries its own AI verdict (per-attachment review, Canvas's
 * per-submission-comment model).
 */
export interface CompletionAttachment {
  id: number;
  completion_id: number;
  file_path: string;
  display_name: string;
  mime_type: string;
  size_bytes: number;
  mime_class: MimeClass;
  thumbnail_path?: string;
  sha256: string;
  sort_order: number;
  deleted_at?: string | null;
  ai_feedback: string;
  ai_confidence: number;
  ai_complete?: boolean | null;
  created_at: string;
}

// --- Task templates (L0) ---

/**
 * A saved chore: the parent tunes it once (points, AI review prompt,
 * reference photo, TTS) and instantiates it repeatedly. Archiving
 * replaces deleting — instantiated chores survive. schedule_json
 * snapshots the cadence so instantiation recreates it.
 */
export interface TaskTemplate {
  id: number;
  name: string;
  category: 'required' | 'core' | 'bonus';
  points: number;
  description: string;
  requires_photo: boolean;
  photo_source: 'child' | 'external' | 'both';
  reference_photo_url?: string;
  tts_audio_url?: string;
  ai_prompt?: string;
  schedule_json: string;
  created_by: number;
  is_archived: boolean;
  created_at: string;
  updated_at: string;
}

/** A template row as listed, with usage counts for the "N uses" badge. */
export interface TaskTemplateWithUsage extends TaskTemplate {
  instance_count: number;
  open_instances: number;
}

/** What instantiate returns: the fresh chore plus its schedule. */
export interface TaskTemplateInstance {
  chore: Chore;
  schedule: ChoreSchedule;
}

/** One day's two-sided coaching note. Kids' views hide parent_text. */
export interface DailyCoachNote {
  user_id: number;
  date: string;
  child_text: string;
  child_audio_url?: string;
  parent_text?: string;
}

// --- The kid's pet (P4-13) ---

/** Pet moods, computed server-side from the kid's chore record. */
export type PetMood = 'happy' | 'calm' | 'hungry';

/** The kid's pet with its server-computed mood and stage progress. */
export interface Pet {
  id: number;
  user_id: number;
  name: string;
  /** 1 young, 2 junior, 3 grown — unlocked by cumulative feeding. */
  stage: 1 | 2 | 3;
  fed_value: number;
  last_fed_at?: string;
  hatched_at: string;
  mood: PetMood;
  /** Fed value the next stage unlocks at (0 once fully grown). */
  next_stage_at: number;
  /** 0-100 toward the next stage. */
  stage_progress: number;
}

/** Before hatching: { hatched: false }. */
export interface PetUnhatched {
  hatched: false;
}

/** What GET /pet returns: the pet, or the "not yet" marker. */
export type PetResponse = Pet | PetUnhatched;

// --- Chore template packs (admin templates page) ---

/** One chore inside an embedded template pack (see internal/config/templates). */
export interface TemplateTask {
  title: string;
  description: string;
  icon?: string;
  /** required | core | bonus — maps to the design categories essential/daily/bonus. */
  category: 'required' | 'core' | 'bonus';
  points: number;
  missed_penalty?: number;
  estimated_minutes?: number;
  /** daily / weekdays / weekends / mon..sun, expanded to schedules on import. */
  days?: string[];
  available_at?: string;
  due_by?: string;
  /** block | no_points | penalty, applied to each imported schedule. */
  expiry?: 'block' | 'no_points' | 'penalty';
}

/** One importable scene×stage task pack, e.g. 初三学习日 or 考试周冲刺包. */
export interface ChoreTemplate {
  id: string;
  name: string;
  icon: string;
  scenario: string;
  description: string;
  tasks: TemplateTask[];
  /** Packs that deliberately drop every bonus chore (exam week). */
  bonus_paused?: boolean;
}

/** The AI tuner's answer: one-line summary plus per-task overrides. */
export interface TemplateTune {
  summary: string;
  changes: {
    index: number;
    title?: string;
    description?: string;
    points?: number;
    estimated_minutes?: number;
  }[];
}

/** One task of an import, as the templates page sends it (tuner-adjusted). */
export interface TemplateImportTask {
  title: string;
  description: string;
  icon: string;
  category: 'required' | 'core' | 'bonus';
  points: number;
  missed_penalty: number;
  estimated_minutes: number;
  days: string[];
  available_at: string;
  due_by: string;
  expiry: 'block' | 'no_points' | 'penalty';
}

/** Import one pack's selected tasks for one kid (schedule swap included). */
export interface TemplateImportRequest {
  template_id: string;
  assigned_to: number;
  tasks: TemplateImportTask[];
  /** End the kid's other open-ended daily schedules (exam-week swap). */
  stop_daily: boolean;
}

/** What the import endpoint returns on 201. */
export interface TemplateImportResult {
  imported: number;
  chores: Chore[];
}

// --- Parent Copilot (P3-10/11) ---

/** One drafted task card. The parent reviews and may edit every field
 *  inline before applying; confidence and rationale are shown so the
 *  suggestion's origin is never a black box. */
export interface CopilotTask {
  title: string;
  description: string;
  category: 'required' | 'core' | 'bonus';
  icon: string;
  points_value: number;
  missed_penalty: number;
  estimated_minutes: number;
  /** daily / weekdays / weekends / mon..sun — same tokens as template packs. */
  days: string[];
  /** One-day task (flyer deadlines): set with empty days. */
  date: string;
  due_by: string;
  available_at: string;
  confidence: number;
  rationale: string;
}

/** A dated fact pulled out of a flyer (期中考试 11月3-5日): surfaced so the
 *  parent can act (e.g. switch template packs), never a chore itself. */
export interface CopilotEvent {
  title: string;
  date: string;
  /** Built-in pack the event implies: 'exam-week' | 'holiday-balance' | ''. */
  template_suggestion: string;
}

/** A parent-side obligation (缴费、家长会): explicitly NOT a kid task. */
export interface CopilotMemo {
  title: string;
  date: string;
}

/** The Copilot's whole answer: cards plus advisory notes (dedup skips). */
export interface CopilotDraft {
  tasks: CopilotTask[];
  events: CopilotEvent[];
  memos: CopilotMemo[];
  notes: string[];
}

/** What the draft endpoint answers: the plan plus the drawer's context. */
export interface CopilotDraftResponse extends CopilotDraft {
  mode: 'text' | 'flyer';
  photo_url?: string;
}

/** One confirmed card of an apply, after the parent's inline edits. */
export interface CopilotApplyTask {
  title: string;
  description: string;
  category: 'required' | 'core' | 'bonus';
  icon: string;
  points_value: number;
  missed_penalty: number;
  estimated_minutes: number;
  days: string[];
  date: string;
  due_by: string;
  available_at: string;
  expiry: 'block' | 'no_points' | 'penalty';
}

/** What the apply endpoint answers on 201. */
export interface CopilotApplyResult {
  applied: number;
  chores: Chore[];
}


// --- Grandparent boost links (P4-16) ---

/**
 * A share-out to a grown-up outside the household: a token-addressed,
 * read-only page with a capped points boost button. The token comes
 * back once at creation (never through the listing) — sharing is a
 * create-time event.
 */
export interface BoostLink {
  id: number;
  user_id: number;
  /** Present only right after creation, when the parent shares it. */
  token?: string;
  label: string;
  monthly_cap: number;
  daily_limit: number;
  granted: number;
  granted_month: string;
  revoked: boolean;
  expires_at: string;
  last_boost_at?: string;
  created_at: string;
}

/** The elder's curated, read-only snapshot (GET/POST /boost/{token}). */
export interface BoostSnapshot {
  kid_name: string;
  label: string;
  current_streak: number;
  week_completed: number;
  coach_line: string;
  monthly_cap: number;
  granted_this_month: number;
  daily_limit: number;
  amounts: number[];
}

// --- L1 error items / wrong-question loop ---

/** Knowledge point on an error item. Adjacency list server-side. */
export interface KnowledgeTag {
  id: number;
  name: string;
  subject: string;
  parent_id?: number;
  code?: string;
  sort_order: number;
  is_system: boolean;
}

/** One wrong question in the kid's error book. */
export interface ErrorItem {
  id: number;
  user_id: number;
  completion_id?: number;
  analysis_id?: number;
  subject: string;
  original_image_url: string;
  ocr_text?: string;
  question_text?: string;
  answer_text?: string;
  wrong_answer_text?: string;
  analysis?: string;
  mistake_analysis?: string;
  mistake_status: 'not_attempted' | 'wrong_attempt' | 'unknown';
  error_type?: 'calculation' | 'concept' | 'reading' | 'method';
  difficulty?: 'easy' | 'medium' | 'hard';
  ai_confidence?: number;
  paper_level?: string;
  grade_semester?: string;
  source?: string;
  mastery_level: 0 | 1 | 2;
  next_review_at?: string;
  created_at: string;
  updated_at: string;
  tags?: KnowledgeTag[];
}

/** The async whole-paper parse job the parent polls. */
export interface PaperAnalysis {
  id: number;
  completion_id: number;
  user_id: number;
  created_by: number;
  subject: string;
  status: 'pending' | 'running' | 'done' | 'failed';
  stage: 'queued' | 'segmenting' | 'judging' | 'done';
  progress: number;
  total_questions: number;
  wrong_count: number;
  error?: string;
  created_at: string;
  updated_at: string;
  /** Present when status is done: the diagnosis report. */
  summary?: PaperAnalysisSummary;
}

/** One question's verdict inside the diagnosis report. */
export interface AnalysisQuestion {
  number: string;
  question_text: string;
  attempted: boolean;
  is_wrong: boolean;
  wrong_answer_text?: string;
  answer_text?: string;
  mistake_analysis?: string;
  error_type?: string;
  knowledge_points?: string[];
  confidence: number;
  error_item_id?: number;
}

/** The diagnosis report stored on a finished analysis. */
export interface PaperAnalysisSummary {
  questions: AnalysisQuestion[];
  error_type_counts: Record<string, number>;
  weak_tags: WeakTagStat[];
  notes?: string[];
}

/** One knowledge point's error tally (the weak-points ranking). */
export interface WeakTagStat {
  tag_id: number;
  name: string;
  subject: string;
  wrong_count: number;
}

/** Error-type distribution row. */
export interface ErrorTypeStat {
  error_type: string;
  count: number;
}

/** Aggregate view of a kid's whole error book. */
export interface ErrorBookReport {
  error_types: ErrorTypeStat[];
  weak_tags: WeakTagStat[];
}

// --- 学习措施引擎 (034) ---

/** One actionable learning measure the parent confirms / strengthens. */
export interface LearningMeasure {
  id: number;
  user_id: number;
  subject: string;
  kc_tag_id?: number;
  kc_name?: string;
  title: string;
  detail: string;
  error_types: string[];
  origin: 'analysis' | 'manual' | 'ai_strengthen';
  origin_id?: number;
  status: 'active' | 'paused' | 'done';
  priority: 1 | 2 | 3;
  created_by: number;
  created_at: string;
  updated_at: string;
}

// --- L4 spaced repetition (FSRS) ---

/** One spaced-repetition card: the go-fsrs Card fields plus the face text. */
export interface ReviewItem {
  id: number;
  user_id: number;
  source_type: 'error_item' | 'kc' | 'course_outcome';
  source_id: number;
  front_text: string;
  back_text?: string;
  subject?: string;
  due: string;
  stability: number;
  difficulty: number;
  scheduled_days: number;
  reps: number;
  lapses: number;
  state: 'New' | 'Learning' | 'Review' | 'Relearning';
  last_review?: string;
  remaining_steps: number;
  suspended: boolean;
  created_at: string;
  /** The resolved "来自：期中卷 · 题干…" line (queue responses only). */
  source_label?: string;
}

/** The kid's live queue: due cards, the upcoming preview, and the 🔥 streak. */
export interface ReviewToday {
  due: ReviewItem[];
  upcoming: ReviewItem[];
  streak: number;
}

/** The per-kid scheduler config (Anki's "most important setting" dial). */
export interface FSRSParams {
  user_id: number;
  request_retention: number;
  maximum_interval: number;
  learning_steps_json: string;
  relearning_steps_json: string;
  enable_fuzz: boolean;
  weights?: string;
  updated_at: string;
}

/** One day of the review heatmap (review_logs by day). */
export interface ReviewHeatmapDay {
  date: string;
  count: number;
  good_count: number;
}

/** One bar of the 30-day load forecast (review_items grouped by due date). */
export interface ReviewForecastBucket {
  date: string;
  count: number;
}

/** One kid's summary row on the parent's mastery dashboard. */
export interface KidReviewStats {
  user_id: number;
  name: string;
  streak: number;
  due_now: number;
  total: number;
}

/** The parent's mastery dashboard data (Anki stats' three forms). */
export interface ReviewStats {
  heatmap: ReviewHeatmapDay[] | null;
  forecast: ReviewForecastBucket[] | null;
  streak: number;
  fsrs_params?: FSRSParams;
  total_cards: number;
  due_now: number;
  per_kid: KidReviewStats[] | null;
}

// --- L2 course system (Canvas Modules / Outcomes) ---

/** A course's lifecycle (edX semantics: drafts are invisible to the kid). */
export type CourseStatus = 'draft' | 'published' | 'archived';

/** One unit's computed state from the unlock engine. */
export type UnitState = 'locked' | 'unlocked' | 'started' | 'completed';

/** The course shell with the kid-side progress joined in (never stored). */
export interface Course {
  id: number;
  user_id: number;
  title: string;
  description: string;
  syllabus_md: string;
  status: CourseStatus;
  is_template: boolean;
  created_at: string;
  updated_at: string;
  // Progress fields carry 0 as a real value the progress bar renders.
  progress_percent: number;
  units_total: number;
  units_completed: number;
}

/** The Canvas Module: position, prerequisites, requirement type, timed unlock. */
export interface CourseUnit {
  id: number;
  course_id: number;
  title: string;
  position: number;
  requirement_type: 'all' | 'one' | '';
  require_sequential_progress: boolean;
  unlock_at?: string | null;
  published: boolean;
}

/** The Canvas ModuleItem: a typed resource plus its completion predicate. */
export interface UnitResource {
  id: number;
  unit_id: number;
  title: string;
  position: number;
  resource_type: 'page' | 'file' | 'audio' | 'video' | 'practice' | 'link' | 'task';
  content_ref: string;
  completion_type?: 'must_view' | 'must_submit' | 'min_score' | 'min_minutes' | '';
  min_score?: number | null;
  min_minutes?: number | null;
}

/** The kid's completion fact row for one (resource, kid). */
export interface ResourceAttempt {
  id: number;
  resource_id: number;
  user_id: number;
  viewed_seconds: number;
  submitted_ref?: string;
  score?: number | null;
  completed_at?: string | null;
  created_at: string;
}

/** One resource as the kid sees it: the resource plus its done flag. */
export type ResourceView = UnitResource & { done: boolean };

/** One unit of the kid's course tree: the unit plus its computed state. */
export type UnitView = CourseUnit & {
  prerequisite_unit_ids: number[];
  state: UnitState;
  lock_explanation?: string;
  resources: ResourceView[];
  completed_resources: number;
  total_resources: number;
};

/** The Canvas Outcome: the mastery line rolled up per calculation_method. */
export interface CourseOutcome {
  id: number;
  course_id: number;
  title: string;
  mastery_points: number;
  calculation_method: 'latest' | 'highest' | 'average' | 'decaying_average';
  calc_int?: number | null;
  ratings_json: string;
}

/** One mastery line with the kid's rollup applied. */
export type OutcomeView = CourseOutcome & {
  /** The rolled-up score; absent when no assessments fed the rollup yet. */
  score?: number | null;
  mastered: boolean;
  assessments: number;
};

/** One score event (Canvas RubricAssessment): who graded, the score. */
export interface OutcomeAssessment {
  id: number;
  outcome_id: number;
  user_id: number;
  score: number;
  assessment_type: 'ai' | 'parent' | 'self';
  artifact_type?: string;
  artifact_id?: number | null;
  comments?: string;
  created_at: string;
}

/** The kid-side course tree (GET /courses and GET /courses/{id}). */
export type CourseTree = Course & {
  units: UnitView[];
  outcomes?: OutcomeView[];
};

/** The admin editor's full tree (GET /admin/courses/{id}). */
export interface AdminCourseTree {
  course: Course;
  units: (CourseUnit & { prerequisite_unit_ids: number[]; resources: UnitResource[] })[];
  outcomes: CourseOutcome[];
}

/** The kid's progress report body (POST /resources/{id}/progress). */
export interface ProgressReport {
  viewed_seconds?: number;
  score?: number;
  submitted_ref?: string;
}

/** What the progress report returns: the attempt plus the fresh unit state. */
export interface ProgressResponse {
  attempt: ResourceAttempt;
  unit: UnitView;
}

// --- L3 learner profile (xAPI events / KC mastery / student config) ---

/** The Ranedeer-style AI teaching config (student_config row). */
export interface StudentConfig {
  user_id: number;
  depth: '小学' | '初中' | '高中';
  learning_style: 'Visual' | 'Verbal' | 'Active' | 'Intuitive' | 'Reflective' | 'Global';
  communication: 'Formal' | 'Textbook' | 'Layman' | 'StoryTelling' | 'Socratic';
  tone: 'Encouraging' | 'Neutral' | 'Informative' | 'Friendly' | 'Humorous';
  reasoning: 'Deductive' | 'Inductive' | 'Abductive' | 'Analogical' | 'Causal';
  /** JSON string arrays, stored verbatim (server validates). */
  interests: string;
  strengths: string;
  weaknesses: string;
  updated_at: string;
}

/** One xAPI-shaped behavior row (append-only; the profile's raw material). */
export interface LearnerEvent {
  id: number;
  user_id: number;
  verb: 'completed' | 'submitted' | 'reviewed' | 'answered' | 'attempted';
  object_type: 'chore' | 'error_item' | 'resource' | 'review_item' | 'course';
  object_id: number;
  result_json?: string;
  context_json?: string;
  occurred_at: string;
  created_at: string;
}

/** One KC row of the profile's matrix (Laplace-smoothed, weakest first). */
export interface KCMasteryStat {
  tag_id: number;
  name: string;
  subject: string;
  correct_count: number;
  total_count: number;
  mastery: number;
  low_confidence: boolean;
  last_event_at?: string | null;
}

/** The profile header's four stats plus the extra counters. */
export interface LearnerHeroStats {
  chores_this_week: number;
  answers_total: number;
  fix_rate: number;
  review_streak: number;
  weak_kc_count: number;
  mastered_items: number;
  reviewed_today: number;
}

/** One day of the 14-day activity trend (zero days included). */
export interface LearnerEventDay {
  day: string;
  events: number;
}

/** GET /admin/learner-profile — the whole profile page in one fetch. */
export interface LearnerProfile {
  kid_id: number;
  kid_name: string;
  config: StudentConfig;
  kc_mastery: KCMasteryStat[];
  events: LearnerEvent[];
  event_trend: LearnerEventDay[];
  hero: LearnerHeroStats;
}

/** GET /kid/ai-feedback — the kid's deterministic feedback card. */
export interface KidAIFeedback {
  weak_kcs: KCMasteryStat[];
  review_streak: number;
  generated_note: string;
  /** The profile one-liner for the error-book AI card title (「视觉型 + 鼓励语气」). */
  config_summary?: string;
}

/** POST /kid/scaffold — one rung of the hint ladder (stage 1..4). */
export interface KidScaffoldHint {
  stage: number;
  hint: string;
}

/** One cited fact under a plan card (DeepTutor's auditability). */
export interface PlannerRef {
  type: 'kc_mastery' | 'learner_events' | 'error_dist' | 'student_config' | 'reward_ladder';
  id?: number;
  fact: string;
}

/** One proposed chore of the planner (the copilot card shape + rationale). */
export interface PlannerChore {
  title: string;
  description: string;
  category: 'required' | 'core' | 'bonus';
  icon: string;
  points_value: number;
  missed_penalty: number;
  estimated_minutes: number;
  days: string[];
  date: string;
  due_by: string;
  available_at: string;
  confidence: number;
  rationale: string;
  /** Per-card citations (1-3 facts justifying THIS chore); falls back to
   *  the plan-level source_refs when the model didn't provide any. */
  refs?: PlannerRef[];
}

/** A proposed review-load bump for one weak KC. */
export interface PlannerReviewBoost {
  kc: string;
  reason: string;
}

/** The planner's structured answer. */
export interface PlannerProposal {
  new_chores: PlannerChore[];
  review_boost: PlannerReviewBoost[];
  notes_to_parent: string;
}

/** The planner's whole answer: cited refs + the proposal. */
export interface PlannerDraft {
  source_refs: PlannerRef[];
  proposal: PlannerProposal;
}

/** POST /admin/copilot/plan — the profile-aware planning answer. */
export interface PlannerPlanResponse {
  plan: PlannerDraft;
  mode: 'learner_plan';
  assigned_to: number;
}

// --- 036: the parent-side media library ---

/** One proof file in the admin media library (GET /api/admin/media). */
export interface MediaItem {
  attachment_id: number;
  completion_id: number;
  file_path: string;
  display_name: string;
  mime_type: string;
  mime_class: MimeClass;
  size_bytes: number;
  created_at: string;
  chore_id: number;
  chore_title: string;
  /** '' groups under "unclassified" (household chores are not school work). */
  subject: string;
  category: string;
  user_id: number;
  user_name: string;
  status: string;
  ai_feedback?: string;
  ai_confidence?: number;
  ai_complete?: boolean | null;
}

export interface MediaSubjectStat {
  subject: string;
  count: number;
}

export interface MediaClassStat {
  mime_class: MimeClass;
  count: number;
}

export interface MediaLibraryResponse {
  items: MediaItem[];
  subjects: MediaSubjectStat[];
  classes: MediaClassStat[];
  total: number;
}
